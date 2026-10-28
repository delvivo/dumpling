package engine

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestParseGrants(t *testing.T) {
	g := parseGrants([]string{
		"GRANT SELECT, RELOAD, PROCESS, REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO `bk`@`%`",
		"GRANT ALL PRIVILEGES ON `shop`.* TO `bk`@`%`", // not global: ignored
	})
	for _, p := range []string{"RELOAD", "PROCESS", "REPLICATION SLAVE", "REPLICATION CLIENT"} {
		if !g[p] {
			t.Errorf("%s missing", p)
		}
	}
	if g["ALL PRIVILEGES"] {
		t.Error("database-level ALL counted as global")
	}
	all := parseGrants([]string{"GRANT ALL PRIVILEGES ON *.* TO `root`@`localhost` WITH GRANT OPTION"})
	if !all["REPLICATION SLAVE"] || !all["RELOAD"] {
		t.Errorf("ALL PRIVILEGES not expanded: %v", all)
	}
	if !parseGrants([]string{"GRANT REPLICATION REPLICA ON *.* TO `x`@`%`"})["REPLICATION SLAVE"] {
		t.Error("REPLICATION REPLICA alias")
	}
}

func TestPosScanner(t *testing.T) {
	for _, line := range []string{
		"-- CHANGE REPLICATION SOURCE TO SOURCE_LOG_FILE='binlog.000042', SOURCE_LOG_POS=157;\n", // 8.4
		"-- CHANGE MASTER TO MASTER_LOG_FILE='binlog.000042', MASTER_LOG_POS=157;\n",             // 8.0
	} {
		var out bytes.Buffer
		s := &posScanner{w: &out, found: make(chan struct{})}
		dump := "-- MySQL dump\n--\n" + line + "CREATE TABLE t (id int);\n"
		for i := 0; i < len(dump); i += 7 { // split writes across line boundaries
			end := min(i+7, len(dump))
			s.Write([]byte(dump[i:end]))
		}
		if s.file != "binlog.000042" || s.pos != 157 || out.String() != dump {
			t.Fatalf("%q: file=%q pos=%d passthrough=%v", line, s.file, s.pos, out.String() == dump)
		}
		select {
		case <-s.found:
		default:
			t.Fatal("found not closed")
		}
	}
}

func TestLockSafety(t *testing.T) {
	rows := parseProcs(strings.Join([]string{
		"1\t500\tSleep\t\t",
		"2\t900\tBinlog Dump\tSource has sent all binlog to replica\t",
		"3\t30\tQuery\texecuting\tSELECT * FROM big",
		"4\t5\tQuery\tupdating\tUPDATE t SET x=1",
	}, "\n"))
	if r := lockUnsafe(rows); r != nil {
		t.Fatalf("unsafe: %+v", r)
	}
	rows = append(rows, procRow{ID: 5, Time: 11, Command: "Query", State: "updating", Info: "update t set x=2"})
	if r := lockUnsafe(rows); r == nil || r.ID != 5 {
		t.Fatalf("long write not detected: %+v", r)
	}

	// watchdog: the dump's FLUSH waiting on the lock for ≥ 5 s is killed, shorter waits are not
	wait := []procRow{{ID: 9, Time: 4, Command: "Query", State: "Waiting for table flush", Info: "FLUSH TABLES WITH READ LOCK"}}
	if lockWaiter(wait, 5*time.Second) != nil {
		t.Fatal("killed before the limit")
	}
	wait[0].Time = 5
	if r := lockWaiter(wait, 5*time.Second); r == nil || r.ID != 9 {
		t.Fatal("not killed at the limit")
	}
	wait[0].Info = "SELECT SLEEP(10)"
	if lockWaiter(wait, 5*time.Second) != nil {
		t.Fatal("killed a non-FLUSH session")
	}
}

func TestBinlogRangeAndArgs(t *testing.T) {
	listed := []string{"binlog.000001", "binlog.000002", "binlog.000003", "binlog.000004"}
	files, ok := binlogRange(listed, "binlog.000002", "binlog.000003")
	if !ok || strings.Join(files, ",") != "binlog.000002,binlog.000003" {
		t.Fatalf("range %v %v", files, ok)
	}
	if _, ok := binlogRange(listed[2:], "binlog.000002", "binlog.000004"); ok {
		t.Fatal("purged start accepted")
	}

	args := binlogArgs(myMarker{Database: "shop", StartPos: 157}, "shop_copy", []string{"a", "b"})
	got := strings.Join(args, " ")
	for _, want := range []string{"--skip-gtids", "--rewrite-db=shop->shop_copy", "--database=shop_copy", "--start-position=157", "a b"} {
		if !strings.Contains(got, want) {
			t.Errorf("args %q lack %q", got, want)
		}
	}
	if strings.Contains(strings.ToLower(got), "password") {
		t.Error("secret on argv")
	}
}
