package revel

import (
	"net"
	"os"
	"strconv"
)

// systemdListenFdsStart は systemd が渡すソケットの最初のファイルディスクリプタ番号
// (sd_listen_fds の SD_LISTEN_FDS_START)。0〜2 は標準入出力なので 3 から並ぶ。
const systemdListenFdsStart = 3

// systemdListener は systemd のソケットアクティベーション(yukicoder.socket など)で
// 渡された待ち受けソケットを返す。渡されていなければ ok=false。
//
// ソケットを systemd が持っていれば、プロセスの再起動の間に来た接続はカーネルの
// キューで待ち、新しいプロセスが Accept したところで処理される。自分で net.Listen
// すると、再起動の間はソケットが無く、nginx からは 502 になる。
//
// LISTEN_PID が自分の PID と一致するときだけ使う(sd_listen_fds と同じ判定)。
// 一致しないのは、親が受け取った環境変数をそのまま引き継いだ子プロセスで、
// その fd 3 は待ち受けソケットではない。使ったら環境変数を消し、この後に起動する
// 子プロセス(ジャッジなど)が同じ判定をしないようにする。
func systemdListener() (l net.Listener, ok bool, err error) {
	pid, err := strconv.Atoi(os.Getenv("LISTEN_PID"))
	if err != nil || pid != os.Getpid() {
		return nil, false, nil
	}
	n, err := strconv.Atoi(os.Getenv("LISTEN_FDS"))
	if err != nil || n < 1 {
		return nil, false, nil
	}
	os.Unsetenv("LISTEN_PID")
	os.Unsetenv("LISTEN_FDS")
	os.Unsetenv("LISTEN_FDNAMES")

	// 1 つ目だけ使う(ユニットでは ListenStream を 1 つだけ書く)
	f := os.NewFile(uintptr(systemdListenFdsStart), "systemd-listen-fd")
	// net.FileListener は fd を複製する(複製側は close-on-exec)。元の fd 3 は
	// close-on-exec が付いておらず子プロセスへ漏れるので、ここで閉じる。
	// 閉じてもソケットそのものは systemd が持ち続ける。
	defer f.Close()
	l, err = net.FileListener(f)
	if err != nil {
		return nil, false, err
	}
	return l, true, nil
}
