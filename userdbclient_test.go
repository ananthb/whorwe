//go:build linux && !android

package whorwe

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestQueryNoUserdb(t *testing.T) {
	cl := &userdbClient{dir: "/non/existent"}
	if _, ok, err := cl.lookupGroup(context.Background(), "stdlibcontrib"); ok {
		t.Fatalf("should fail but lookup has been handled or error is nil: %v", err)
	}
}

type userdbTestData map[string]udbResponse

type udbResponse struct {
	data  []byte
	delay time.Duration
}

func userdbServer(ctx context.Context, t *testing.T, sockFn string, data userdbTestData) {
	ready := make(chan struct{})
	go func() {
		if err := serveUserdb(ctx, ready, sockFn, data); err != nil {
			t.Error(err)
		}
	}()
	<-ready
}

func (u userdbTestData) String() string {
	var s strings.Builder
	for k, v := range u {
		s.WriteString("Request:\n")
		s.WriteString(k)
		s.WriteString("\nResponse:\n")
		if v.delay > 0 {
			s.WriteString("Delay: ")
			s.WriteString(v.delay.String())
			s.WriteString("\n")
		}
		s.WriteString("Data:\n")
		s.Write(v.data)
		s.WriteString("\n")
	}
	return s.String()
}

// serverUserdb is a simple userdb server that replies to VARLINK method calls.
// A message is sent on the ready channel when the server is ready to accept calls.
// The server will reply to each request in the data map. If a request is not
// found in the map, the server will return an error.
func serveUserdb(
	ctx context.Context,
	ready chan<- struct{},
	sockFn string,
	data userdbTestData,
) error {
	sockFd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(sockFd)
	if err := syscall.Bind(sockFd, &syscall.SockaddrUnix{Name: sockFn}); err != nil {
		return err
	}
	if err := syscall.Listen(sockFd, 1); err != nil {
		return err
	}

	// Send ready signal.
	ready <- struct{}{}

	var srvGroup sync.WaitGroup

	srvErrs := make(chan error, len(data))
	for len(data) != 0 {
		if err := ctx.Err(); err != nil && err != context.Canceled {
			return err
		}
		nfd, _, err := syscall.Accept(sockFd)
		if err != nil {
			syscall.Close(nfd)
			return err
		}

		// Read request.
		buf := make([]byte, 4096)
		n, err := syscall.Read(nfd, buf)
		if err != nil {
			syscall.Close(nfd)
			return err
		}
		if n == 0 {
			// Client went away.
			continue
		}
		if buf[n-1] != 0 {
			syscall.Close(nfd)
			return errors.New("request not null terminated")
		}
		// Remove null terminator.
		buf = buf[:n-1]
		got := string(buf)

		// Fetch response for request.
		response, ok := data[got]
		if !ok {
			syscall.Close(nfd)
			msg := "unexpected request:\n" + got + "\n\ndata:\n" + data.String()
			return errors.New(msg)
		}
		delete(data, got)

		srvGroup.Add(1)
		go func() {
			defer srvGroup.Done()
			if err := serveClient(nfd, response); err != nil {
				srvErrs <- err
			}
		}()
	}

	srvGroup.Wait()
	// Combine serve errors if any.
	if len(srvErrs) > 0 {
		var errs []error
		for err := range srvErrs {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}

	return nil
}

func serveClient(fd int, response udbResponse) error {
	defer syscall.Close(fd)
	time.Sleep(response.delay)
	data := response.data
	if len(data) != 0 && data[len(data)-1] != 0 {
		data = append(data, 0)
	}
	written := 0
	for written < len(data) {
		if n, err := syscall.Write(fd, data[written:]); err != nil {
			return err
		} else {
			written += n
		}
	}
	return nil
}

func TestSlowUserdbLookup(t *testing.T) {
	tmpdir := t.TempDir()
	data := userdbTestData{
		`{"method":"io.systemd.UserDatabase.GetGroupRecord","parameters":{"service":"io.systemd.Multiplexer","groupName":"stdlibcontrib"}}`: udbResponse{
			delay: time.Hour,
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	userdbServer(ctx, t, tmpdir+"/"+svcMultiplexer, data)
	cl := &userdbClient{dir: tmpdir}
	// Lookup should timeout.
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Microsecond)
	defer cancel2()
	if _, ok, _ := cl.lookupGroup(ctx2, "stdlibcontrib"); ok {
		t.Fatalf("lookupGroup should not be handled but was")
	}
}

func TestFastestUserdbLookup(t *testing.T) {
	tmpdir := t.TempDir()
	fastData := userdbTestData{
		`{"method":"io.systemd.UserDatabase.GetGroupRecord","parameters":{"service":"fast","groupName":"stdlibcontrib"}}`: udbResponse{
			data: []byte(
				`{"parameters":{"record":{"groupName":"stdlibcontrib","gid":181,"members":["stdlibcontrib"],"status":{"ecb5a44f1a5846ad871566e113bf8937":{"service":"io.systemd.NameServiceSwitch"}}},"incomplete":false}}`,
			),
		},
	}
	slowData := userdbTestData{
		`{"method":"io.systemd.UserDatabase.GetGroupRecord","parameters":{"service":"slow","groupName":"stdlibcontrib"}}`: udbResponse{
			delay: 50 * time.Millisecond,
			data: []byte(
				`{"parameters":{"record":{"groupName":"stdlibcontrib","gid":182,"members":["stdlibcontrib"],"status":{"ecb5a44f1a5846ad871566e113bf8937":{"service":"io.systemd.NameServiceSwitch"}}},"incomplete":false}}`,
			),
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	userdbServer(ctx, t, tmpdir+"/"+"fast", fastData)
	userdbServer(ctx, t, tmpdir+"/"+"slow", slowData)
	cl := &userdbClient{dir: tmpdir}
	group, ok, err := cl.lookupGroup(context.Background(), "stdlibcontrib")
	if !ok {
		t.Fatalf("lookup should be handled but was not")
	}
	if err != nil {
		t.Fatalf("lookup should not fail but did: %v", err)
	}
	if group.Gid != "181" {
		t.Fatalf("lookup should return group 181 but returned %s", group.Gid)
	}
}

func TestUserdbLookupGroup(t *testing.T) {
	tmpdir := t.TempDir()
	data := userdbTestData{
		`{"method":"io.systemd.UserDatabase.GetGroupRecord","parameters":{"service":"io.systemd.Multiplexer","groupName":"stdlibcontrib"}}`: udbResponse{
			data: []byte(
				`{"parameters":{"record":{"groupName":"stdlibcontrib","gid":181,"members":["stdlibcontrib"],"status":{"ecb5a44f1a5846ad871566e113bf8937":{"service":"io.systemd.NameServiceSwitch"}}},"incomplete":false}}`,
			),
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	userdbServer(ctx, t, tmpdir+"/"+svcMultiplexer, data)

	groupname := "stdlibcontrib"
	want := &Group{
		Name: "stdlibcontrib",
		Gid:  "181",
	}
	cl := &userdbClient{dir: tmpdir}
	got, ok, err := cl.lookupGroup(context.Background(), groupname)
	if !ok {
		t.Fatal("lookup should have been handled")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lookupGroup(%s) = %v, want %v", groupname, got, want)
	}
}

func TestUserdbLookupUser(t *testing.T) {
	tmpdir := t.TempDir()
	data := userdbTestData{
		`{"method":"io.systemd.UserDatabase.GetUserRecord","parameters":{"service":"io.systemd.Multiplexer","userName":"stdlibcontrib"}}`: udbResponse{
			data: []byte(
				`{"parameters":{"record":{"userName":"stdlibcontrib","uid":181,"gid":181,"realName":"Stdlib Contrib","homeDirectory":"/home/stdlibcontrib","status":{"ecb5a44f1a5846ad871566e113bf8937":{"service":"io.systemd.NameServiceSwitch"}}},"incomplete":false}}`,
			),
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	userdbServer(ctx, t, tmpdir+"/"+svcMultiplexer, data)

	username := "stdlibcontrib"
	want := &User{
		Uid:      "181",
		Gid:      "181",
		Username: "stdlibcontrib",
		Name:     "Stdlib Contrib",
		HomeDir:  "/home/stdlibcontrib",
	}
	cl := &userdbClient{dir: tmpdir}
	got, ok, err := cl.lookupUser(context.Background(), username)
	if !ok {
		t.Fatal("lookup should have been handled")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lookupUser(%s) = %v, want %v", username, got, want)
	}
}

func TestUserdbLookupGroupIds(t *testing.T) {
	tmpdir := t.TempDir()
	data := userdbTestData{
		`{"method":"io.systemd.UserDatabase.GetMemberships","parameters":{"service":"io.systemd.Multiplexer","userName":"stdlibcontrib"},"more":true}`: udbResponse{
			data: []byte(
				`{"parameters":{"userName":"stdlibcontrib","groupName":"stdlib"},"continues":true}` + "\x00" + `{"parameters":{"userName":"stdlibcontrib","groupName":"contrib"}}`,
			),
		},
		// group records
		`{"method":"io.systemd.UserDatabase.GetGroupRecord","parameters":{"service":"io.systemd.Multiplexer","groupName":"stdlibcontrib"}}`: udbResponse{
			data: []byte(
				`{"parameters":{"record":{"groupName":"stdlibcontrib","members":["stdlibcontrib"],"gid":181,"status":{"ecb5a44f1a5846ad871566e113bf8937":{"service":"io.systemd.NameServiceSwitch"}}},"incomplete":false}}`,
			),
		},
		`{"method":"io.systemd.UserDatabase.GetGroupRecord","parameters":{"service":"io.systemd.Multiplexer","groupName":"stdlib"}}`: udbResponse{
			data: []byte(
				`{"parameters":{"record":{"groupName":"stdlib","members":["stdlibcontrib"],"gid":182,"status":{"ecb5a44f1a5846ad871566e113bf8937":{"service":"io.systemd.NameServiceSwitch"}}},"incomplete":false}}`,
			),
		},
		`{"method":"io.systemd.UserDatabase.GetGroupRecord","parameters":{"service":"io.systemd.Multiplexer","groupName":"contrib"}}`: udbResponse{
			data: []byte(
				`{"parameters":{"record":{"groupName":"contrib","members":["stdlibcontrib"],"gid":183,"status":{"ecb5a44f1a5846ad871566e113bf8937":{"service":"io.systemd.NameServiceSwitch"}}},"incomplete":false}}`,
			),
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	userdbServer(ctx, t, tmpdir+"/"+svcMultiplexer, data)

	username := "stdlibcontrib"
	want := []string{"181", "182", "183"}
	cl := &userdbClient{dir: tmpdir}
	got, ok, err := cl.lookupGroupIds(context.Background(), username)
	if !ok {
		t.Fatal("lookup should have been handled")
	}
	if err != nil {
		t.Fatal(err)
	}
	// Result order is not specified so sort it.
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lookupGroupIds(%s) = %v, want %v", username, got, want)
	}
}
