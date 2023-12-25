//go:build linux && !android

package whorwe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"strconv"
	"sync"
	"syscall"
)

const (
	// Well known multiplexer service.
	svcMultiplexer = "io.systemd.Multiplexer"

	userdbNamespace = "io.systemd.UserDatabase"

	// io.systemd.UserDatabase VARLINK interface methods.
	mGetGroupRecord = userdbNamespace + ".GetGroupRecord"
	mGetUserRecord  = userdbNamespace + ".GetUserRecord"
	mGetMemberships = userdbNamespace + ".GetMemberships"

	// io.systemd.UserDatabase VARLINK interface errors.
	errNoRecordFound       = userdbNamespace + ".NoRecordFound"
	errServiceNotAvailable = userdbNamespace + ".ServiceNotAvailable"
)

const userdbClientDir = "/run/systemd/userdb"

// userdbCall represents a VARLINK service call sent to systemd-userdb.
// method is the VARLINK method to call.
// parameters are the VARLINK parameters to pass.
// more indicates if more responses are expected.
// fastest indicates if only the fastest response should be returned.
type userdbCall struct {
	method     string
	parameters callParameters
	more       bool
	fastest    bool
}

func (u userdbCall) marshalJSON(service string) ([]byte, error) {
	params, err := u.parameters.marshalJSON(service)
	if err != nil {
		return nil, err
	}
	var data bytes.Buffer
	data.WriteString(`{"method":"`)
	data.WriteString(u.method)
	data.WriteString(`","parameters":`)
	data.Write(params)
	if u.more {
		data.WriteString(`,"more":true`)
	}
	data.WriteString(`}`)
	return data.Bytes(), nil
}

type callParameters struct {
	uid       *int64
	userName  string
	gid       *int64
	groupName string
}

func (c callParameters) marshalJSON(service string) ([]byte, error) {
	var data bytes.Buffer
	data.WriteString(`{"service":"`)
	data.WriteString(service)
	data.WriteString(`"`)
	if c.uid != nil {
		data.WriteString(`,"uid":`)
		data.WriteString(strconv.FormatInt(*c.uid, 10))
	}
	if c.userName != "" {
		data.WriteString(`,"userName":"`)
		data.WriteString(c.userName)
		data.WriteString(`"`)
	}
	if c.gid != nil {
		data.WriteString(`,"gid":`)
		data.WriteString(strconv.FormatInt(*c.gid, 10))
	}
	if c.groupName != "" {
		data.WriteString(`,"groupName":"`)
		data.WriteString(c.groupName)
		data.WriteString(`"`)
	}
	data.WriteString(`}`)
	return data.Bytes(), nil
}

type userdbReply struct {
	continues  bool
	errorStr   string
	parameters map[string]any
}

func (u *userdbReply) unmarshalJSON(data []byte) error {
	obj, _, err := parseJSONObject(data)
	if err != nil {
		return err
	}
	if continues, ok := obj["continues"]; ok {
		u.continues, ok = continues.(bool)
		if !ok {
			return fmt.Errorf("continues is not a boolean: %v", continues)
		}
	}
	if errorStr, ok := obj["error"]; ok {
		u.errorStr, ok = errorStr.(string)
		if !ok {
			return fmt.Errorf("error is not a string: %v", errorStr)
		}
	}
	if parameters, ok := obj["parameters"]; ok {
		u.parameters, ok = parameters.(map[string]any)
		if !ok {
			return fmt.Errorf("parameters is not a map: %v", parameters)
		}
	}
	return nil
}

// response holds parsed replies resulting from a method call to systemd-userdb.
// handled indicates if the call was handled by systemd-userdb.
// err is any error encountered.
type response struct {
	replies []userdbReply
	handled bool
	err     error
}

// querySocket calls the io.systemd.UserDatabase VARLINK interface at sock with request.
// Multiple replies can be fetched by setting more to true in the request.
// Replies with io.systemd.UserDatabase.NoRecordFound errors are skipped.
// Other UserDatabase errors are returned as is.
// If the socket does not exist, or if the io.systemd.UserDatabase.ServiceNotAvailable
// error is seen in a response, the query is considered unhandled.
func querySocket(ctx context.Context, sock string, request []byte) response {
	sockFd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return response{err: err}
	}
	defer syscall.Close(sockFd)
	if err := syscall.Connect(sockFd, &syscall.SockaddrUnix{Name: sock}); err != nil {
		if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ENOTDIR) {
			return response{err: err}
		}
		return response{handled: true, err: err}
	}

	// Null terminate request.
	if request[len(request)-1] != 0 {
		request = append(request, 0)
	}

	// Write request to socket.
	written := 0
	for written < len(request) {
		if ctx.Err() != nil {
			return response{handled: true, err: ctx.Err()}
		}
		if n, err := syscall.Write(sockFd, request[written:]); err != nil {
			return response{handled: true, err: err}
		} else {
			written += n
		}
	}

	// Read response.
	var resp bytes.Buffer
	for {
		if ctx.Err() != nil {
			return response{handled: true, err: ctx.Err()}
		}
		buf := make([]byte, 4096)
		if n, err := syscall.Read(sockFd, buf); err != nil {
			return response{handled: true, err: err}
		} else if n > 0 {
			resp.Write(buf[:n])
			if buf[n-1] == 0 {
				break
			}
		} else {
			// EOF
			break
		}
	}

	if resp.Len() == 0 {
		return response{handled: true, err: fmt.Errorf("empty response")}
	}

	buf := resp.Bytes()
	// Remove trailing 0.
	buf = buf[:len(buf)-1]
	// Split into VARLINK messages.
	msgs := bytes.Split(buf, []byte{0})

	var replies []userdbReply
	// Parse VARLINK messages.
	for _, m := range msgs {
		var reply userdbReply
		if err := reply.unmarshalJSON(m); err != nil {
			return response{handled: true, err: err}
		}
		// Handle VARLINK message errors.
		switch e := reply.errorStr; e {
		case "":
		case errNoRecordFound: // Ignore not found error.
			continue
		case errServiceNotAvailable:
			return response{}
		default:
			return response{handled: true, err: errors.New(e)}
		}
		replies = append(replies, reply)
		if !reply.continues {
			break
		}
	}
	return response{replies: replies, handled: true, err: ctx.Err()}
}

// unmarshaler is an interface for types that can unmarshal themselves from a slice of
// userdb reply parameters.
type unmarshaler interface {
	unmarshal([]map[string]any) error
}

// queryMany calls the io.systemd.UserDatabase VARLINK interface on many services at once.
// ss is a slice of userdb services to call. Each service must have a socket in cl.dir.
// c is sent to all services in ss. If c.fastest is true, only the fastest reply is read.
// Otherwise all replies are aggregated. um is called with aggregated reply parameters.
// queryMany returns the first error encountered. The first result is false if no userdb
// socket is available or if all requests time out.
func queryMany(ctx context.Context, ss []string, c *userdbCall, um unmarshaler) (bool, error) {
	responseCh := make(chan response, len(ss))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Query all services in parallel.
	var workers sync.WaitGroup
	for _, svc := range ss {
		data, err := c.marshalJSON(svc)
		if err != nil {
			return true, err
		}
		// Spawn worker to query service.
		workers.Add(1)
		go func(sock string, data []byte) {
			defer workers.Done()
			responseCh <- querySocket(ctx, sock, data)
		}(userdbClientDir+"/"+svc, data)
	}

	go func() {
		// Clean up workers.
		workers.Wait()
		close(responseCh)
	}()

	// result holds the aggregated reply parameters.
	var result []map[string]any
	var notOk int
RecvResponses:
	for {
		select {
		case resp, ok := <-responseCh:
			if !ok {
				// Responses channel is closed so stop reading.
				break RecvResponses
			}
			if resp.err != nil {
				// querySocket only returns unrecoverable errors,
				// so return the first one received.
				return true, resp.err
			}
			if !resp.handled {
				notOk++
				continue
			}

			first := len(result) == 0
			for _, r := range resp.replies {
				result = append(result, r.parameters)
			}
			if first && c.fastest {
				// Return the fastest response.
				break RecvResponses
			}
		case <-ctx.Done():
			// If requests time out, userdb is unavailable.
			return ctx.Err() != context.DeadlineExceeded, nil
		}
	}
	// If all sockets are not ok, userdb is unavailable.
	if notOk == len(ss) {
		return false, nil
	}
	return true, um.unmarshal(result)
}

// services enumerates userdb service sockets in dir.
// If ok is false, io.systemd.UserDatabase service is unavailable.
func services() (s []string, ok bool, err error) {
	var entries []fs.DirEntry
	if entries, err = os.ReadDir(userdbClientDir); err != nil {
		ok = !os.IsNotExist(err)
		return
	}
	if len(entries) == 0 {
		return
	}
	for _, ent := range entries {
		s = append(s, ent.Name())
	}
	ok = true
	return
}

// query looks up users/groups on the io.systemd.UserDatabase VARLINK interface.
// If the multiplexer service is available, the call is sent only to it.
// Otherwise, the call is sent simultaneously to all UserDatabase services in cl.dir.
// The first request that succeeds is read and unmarshalled. All other requests are cancelled.
// If the service is unavailable, the first result is false.
// The service is considered unavailable if the requests time-out as well.
func query(ctx context.Context, call *userdbCall, um unmarshaler) (bool, error) {
	svcs := []string{svcMultiplexer}
	if _, err := os.Stat(userdbClientDir + "/" + svcMultiplexer); err != nil {
		// No mux service so call all available services.
		var ok bool
		if svcs, ok, err = services(); !ok || err != nil {
			return ok, err
		}
	}
	call.fastest = true
	if ok, err := queryMany(ctx, svcs, call, um); !ok || err != nil {
		return ok, err
	}
	return true, nil
}

type groupRecord struct {
	groupName string
	gid       int64
}

// unmarshal parses a group record from a userdb reply.
func (g *groupRecord) unmarshal(objects []map[string]any) error {
	if len(objects) != 1 {
		return errors.New("invalid group record")
	}
	record, ok := objects[0]["record"]
	if !ok {
		return errors.New("missing group record")
	}
	rmap, ok := record.(map[string]any)
	if !ok {
		return errors.New("invalid group record")
	}
	groupname, ok := rmap["groupName"]
	if !ok {
		return errors.New("missing group name")
	}
	g.groupName, ok = groupname.(string)
	if !ok {
		return errors.New("invalid group name")
	}
	gid, ok := rmap["gid"]
	if !ok {
		return errors.New("missing gid")
	}
	g.gid, ok = gid.(int64)
	if !ok {
		return errors.New("invalid gid")
	}
	return nil
}

// queryGroupDb queries the userdb interface for a gid, groupname, or both.
func queryGroupDb(ctx context.Context, gid *int64, groupname string) (*user.Group, bool, error) {
	group := groupRecord{}
	request := userdbCall{
		method:     mGetGroupRecord,
		parameters: callParameters{gid: gid, groupName: groupname},
	}
	if ok, err := query(ctx, &request, &group); !ok || err != nil {
		return nil, ok, fmt.Errorf("error querying systemd-userdb group record: %s", err)
	}
	return &user.Group{
		Name: group.groupName,
		Gid:  strconv.FormatInt(group.gid, 10),
	}, true, nil
}

type userRecord struct {
	userName      string
	realName      string
	uid           int64
	gid           int64
	homeDirectory string
}

// unmarshal parses a user record from a userdb reply.
func (u *userRecord) unmarshal(objects []map[string]any) error {
	if len(objects) != 1 {
		return errors.New("invalid user record")
	}
	record, ok := objects[0]["record"]
	if !ok {
		return errors.New("missing user record")
	}
	rmap, ok := record.(map[string]any)
	if !ok {
		return errors.New("invalid user record")
	}

	username, ok := rmap["userName"]
	if !ok {
		return errors.New("missing user name")
	}
	r.userName, ok = username.(string)
	if !ok {
		return errors.New("invalid user name")
	}

	realname, ok := rmap["realName"]
	if !ok {
		return errors.New("missing real name")
	}
	r.realName, ok = realname.(string)
	if !ok {
		return errors.New("invalid real name")
	}

	uid, ok := rmap["uid"]
	if !ok {
		return errors.New("missing uid")
	}
	u.uid, ok = uid.(int64)
	if !ok {
		return errors.New("invalid uid")
	}

	gid, ok := rmap["gid"]
	if !ok {
		return errors.New("missing gid")
	}
	u.gid, ok = gid.(int64)
	if !ok {
		return errors.New("invalid gid")
	}

	homedir, ok := rmap["homeDirectory"]
	if !ok {
		return errors.New("missing home directory")
	}
	u.homeDirectory, ok = homedir.(string)
	if !ok {
		return errors.New("invalid home directory")
	}
	return nil
}

// queryUserDb queries the userdb interface for a uid, username, or both.
func queryUserDb(ctx context.Context, uid *int64, username string) (*user.User, bool, error) {
	user := userRecord{}
	request := userdbCall{
		method: mGetUserRecord,
		parameters: callParameters{
			uid:      uid,
			userName: username,
		},
	}
	if ok, err := query(ctx, &request, &user); !ok || err != nil {
		return nil, ok, fmt.Errorf("error querying systemd-userdb user record: %s", err)
	}
	return &user.User{
		Uid:      strconv.FormatInt(user.uid, 10),
		Gid:      strconv.FormatInt(user.gid, 10),
		Username: user.userName,
		Name:     user.realName,
		HomeDir:  user.homeDirectory,
	}, true, nil
}

func lookupGroup(ctx context.Context, groupname string) (*user.Group, bool, error) {
	return queryGroupDb(ctx, nil, groupname)
}

func lookupGroupId(ctx context.Context, id string) (*user.Group, bool, error) {
	gid, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil, true, err
	}
	return queryGroupDb(ctx, &gid, "")
}

func lookupUser(ctx context.Context, username string) (*user.User, bool, error) {
	return queryUserDb(ctx, nil, username)
}

func lookupUserId(ctx context.Context, id string) (*user.User, bool, error) {
	uid, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil, true, err
	}
	return queryUserDb(ctx, &uid, "")
}

type memberships struct {
	// Keys are groupNames and values are sets of userNames.
	groupUsers map[string]map[string]struct{}
}

// unmarshal creates a map of group names to user names from many userdb replies.
func (m *memberships) unmarshal(objects []map[string]any) error {
	if m.groupUsers == nil {
		m.groupUsers = make(map[string]map[string]struct{})
	}
	var (
		kUserName  = []byte(`"userName"`)
		kGroupName = []byte(`"groupName"`)
	)
	for _, obj := range objects {
		record, ok := obj["record"]
		if !ok {
			return errors.New("missing membership record")
		}
		rmap, ok := record.(map[string]any)
		if !ok {
			return errors.New("invalid membership record")
		}

		var groupName string
		var userName string

		uname, ok := rmap["userName"]
		if !ok {
			return errors.New("missing user name")
		}
		userName, ok = username.(string)
		if !ok {
			return errors.New("invalid user name")
		}

		gname, ok := rmap["groupName"]
		if !ok {
			return errors.New("missing group name")
		}
		groupName, ok = gname.(string)
		if !ok {
			return errors.New("invalid group name")
		}

		// Associate userName with groupName.
		if groupName != "" && userName != "" {
			if _, ok := m.groupUsers[groupName]; ok {
				m.groupUsers[groupName][userName] = struct{}{}
			} else {
				m.groupUsers[groupName] = map[string]struct{}{userName: {}}
			}
		}
	}
	return nil
}

func lookupGroupIds(ctx context.Context, username string) ([]string, bool, error) {
	services, ok, err := services()
	if !ok || err != nil {
		return nil, ok, err
	}
	// Fetch group memberships for username.
	var ms memberships
	request := userdbCall{
		method:     mGetMemberships,
		parameters: callParameters{userName: username},
		more:       true,
	}
	if ok, err := queryMany(ctx, services, &request, &ms); !ok || err != nil {
		return nil, ok, fmt.Errorf("error querying systemd-userdb memberships record: %s", err)
	}
	// Fetch user group gid.
	var group groupRecord
	request = userdbCall{
		method:     mGetGroupRecord,
		parameters: callParameters{groupName: username},
	}
	if ok, err := query(ctx, &request, &group); !ok || err != nil {
		return nil, ok, err
	}
	gids := []string{strconv.FormatInt(group.gid, 10)}

	// Fetch group records for each group.
	for g := range ms.groupUsers {
		var group groupRecord
		request.parameters.groupName = g
		// Query group for gid.
		if ok, err := query(ctx, &request, &group); !ok || err != nil {
			return nil, ok, fmt.Errorf("error querying systemd-userdb group record: %s", err)
		}
		gids = append(gids, strconv.FormatInt(group.gid, 10))
	}
	return gids, true, nil
}
