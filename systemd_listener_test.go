package revel

import (
	"bufio"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// systemd の代わりに、待ち受けソケットを fd 3 に付けて子プロセス(このテストバイナリ)を起動し、
// 子が systemdListener でそれを受け取って Accept できることを確かめる。
// LISTEN_PID は起動前には分からないので、子が自分で入れる(SYSTEMD_LISTENER_TEST_CHILD)。
func TestSystemdListenerAcceptsPassedSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ExtraFiles(fd の受け渡し)は Windows では使えない")
	}
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f, err := ln.(*net.UnixListener).File()
	if err != nil {
		t.Fatal(err)
	}
	// 親の側は閉じても、子に渡した複製でソケットは生きている(systemd が持ち続けるのと同じ)
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^TestSystemdListenerChild$")
	cmd.Env = append(os.Environ(), "SYSTEMD_LISTENER_TEST_CHILD=1", "LISTEN_FDS=1")
	cmd.ExtraFiles = []*os.File{f} // 子では fd 3 になる
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer cmd.Wait()

	// 子が受け取ったと言うまで待ってから、ソケットファイル経由でつなぐ
	r := bufio.NewReader(out)
	line, err := r.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("子の出力 = %q, %v", line, err)
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("渡したソケットにつながらない: %v", err)
	}
	defer c.Close()
	line, err = bufio.NewReader(c).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "hello" {
		t.Fatalf("子からの応答 = %q, %v", line, err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("子が失敗した: %v", err)
	}
}

// 再起動の間(待ち受けるプロセスがいない間)につないだ接続が、拒否されずにカーネルの
// キューで待ち、次に起動したプロセスが応答すること。systemd がソケットを持ち続けて
// サービスだけ再起動するのと同じ形を、親(= systemd)と子 2 つで作る。
func TestSystemdListenerQueuesDuringRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ExtraFiles(fd の受け渡し)は Windows では使えない")
	}
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	f, err := ln.(*net.UnixListener).File() // 親が最後まで持つ(systemd の役)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ln.Close()

	start := func() (*exec.Cmd, *bufio.Reader) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestSystemdListenerChild$")
		cmd.Env = append(os.Environ(), "SYSTEMD_LISTENER_TEST_CHILD=1", "LISTEN_FDS=1")
		cmd.ExtraFiles = []*os.File{f}
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return cmd, bufio.NewReader(out)
	}
	ask := func(c net.Conn) {
		t.Helper()
		defer c.Close()
		line, err := bufio.NewReader(c).ReadString('\n')
		if err != nil || strings.TrimSpace(line) != "hello" {
			t.Fatalf("応答 = %q, %v", line, err)
		}
	}

	// 1 つ目の子: 1 回応答して終わる(= 再起動のために止まる)
	first, r := start()
	if line, _ := r.ReadString('\n'); strings.TrimSpace(line) != "ready" {
		t.Fatalf("1 つ目の子の出力 = %q", line)
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ask(c)
	if err := first.Wait(); err != nil {
		t.Fatalf("1 つ目の子が失敗した: %v", err)
	}

	// 待ち受けるプロセスがいない間につなぐ。自分で net.Listen していた頃はここで
	// ソケットが無く失敗していた(nginx の 502)
	c, err = net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("再起動の間の接続が拒否された: %v", err)
	}

	// 2 つ目の子が、待っていた接続に応答する
	second, _ := start()
	ask(c)
	if err := second.Wait(); err != nil {
		t.Fatalf("2 つ目の子が失敗した: %v", err)
	}
}

// TestSystemdListenerChild は上のテストが子プロセスとして起動する。単独では何もしない。
func TestSystemdListenerChild(t *testing.T) {
	if os.Getenv("SYSTEMD_LISTENER_TEST_CHILD") != "1" {
		t.Skip("子プロセスとしてだけ動く")
	}
	os.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	l, ok, err := systemdListener()
	if err != nil || !ok {
		t.Fatalf("systemdListener = ok %v, err %v", ok, err)
	}
	// 子プロセスへ引き継がないよう、環境変数は消えている
	for _, k := range []string{"LISTEN_PID", "LISTEN_FDS"} {
		if v, set := os.LookupEnv(k); set {
			t.Errorf("%s が残っている: %q", k, v)
		}
	}
	os.Stdout.WriteString("ready\n")
	c, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("hello\n"))
	c.Close()
}

// LISTEN_PID が自分でなければ使わない(親の環境変数を引き継いだ子プロセスの場合)。
func TestSystemdListenerIgnoresOtherPid(t *testing.T) {
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()+1))
	t.Setenv("LISTEN_FDS", "1")
	if _, ok, err := systemdListener(); ok || err != nil {
		t.Fatalf("systemdListener = ok %v, err %v, want 使わない", ok, err)
	}
	if os.Getenv("LISTEN_FDS") != "1" {
		t.Error("使わないときに環境変数を消した")
	}
}

// 環境変数が無ければ使わない(今までどおり自分で net.Listen する)。
func TestSystemdListenerWithoutEnv(t *testing.T) {
	t.Setenv("LISTEN_PID", "")
	t.Setenv("LISTEN_FDS", "")
	if _, ok, err := systemdListener(); ok || err != nil {
		t.Fatalf("systemdListener = ok %v, err %v, want 使わない", ok, err)
	}
}
