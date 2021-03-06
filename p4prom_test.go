package main

import (
	"context"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	p4dlog "github.com/rcowham/go-libp4dlog"
	"github.com/rcowham/go-libtail/tailer/fswatcher"
	"github.com/rcowham/p4prometheus/config"
	"github.com/sirupsen/logrus"
)

var (
	eol    = regexp.MustCompile("\r\n|\n")
	logger = &logrus.Logger{Out: os.Stderr,
		Formatter: &logrus.TextFormatter{TimestampFormat: "15:04:05.000", FullTimestamp: true},
		Level:     logrus.InfoLevel}
)

func getResult(output chan string) []string {
	lines := []string{}
	for line := range output {
		lines = append(lines, line)
	}
	return lines
}

// Implementation of tailer for testing
type mockTailer struct {
	linechan  chan *fswatcher.Line
	errorchan chan *fswatcher.Error
}

func (t *mockTailer) Lines() chan *fswatcher.Line {
	return t.linechan
}
func (t *mockTailer) Close() {
	close(t.linechan)
}
func (t *mockTailer) Errors() chan fswatcher.Error {
	return nil
}

func newMockTailer() *mockTailer {
	return &mockTailer{
		linechan:  make(chan *fswatcher.Line, 1000),
		errorchan: make(chan *fswatcher.Error, 10),
	}
}
func (t *mockTailer) addLines(lines []string) {
	for _, l := range lines {
		// t.lines = append(t.lines, l)
		t.linechan <- &fswatcher.Line{Line: l, File: ""}
	}
}

func funcName() string {
	fpcs := make([]uintptr, 1)
	// Skip 2 levels to get the caller
	n := runtime.Callers(2, fpcs)
	if n == 0 {
		return ""
	}
	caller := runtime.FuncForPC(fpcs[0] - 1)
	if caller == nil {
		return ""
	}
	return caller.Name()
}

// Assuming there are several outputs - this returns the latest one
func getOutput(testchan chan string) []string {
	result := make([]string, 0)
	lastoutput := ""
	for output := range testchan {
		lastoutput = output
	}
	for _, line := range eol.Split(lastoutput, -1) {
		if len(line) > 0 && !strings.HasPrefix(line, "#") {
			result = append(result, line)
		}
	}
	sort.Strings(result)
	return result
}

func basicTest(t *testing.T, cfg *config.Config, input string) []string {
	logrus.SetFormatter(&logrus.TextFormatter{TimestampFormat: "15:04:05.000", FullTimestamp: true})
	logger.SetReportCaller(true)
	logger.Infof("Function: %s", funcName())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fp := p4dlog.NewP4dFileParser(logger)
	// fp.SetDebugMode()
	fp.SetDurations(10*time.Millisecond, 20*time.Millisecond)
	lines := make(chan []byte, 100)
	metrics := make(chan string, 100)
	p4p := newP4Prometheus(cfg, logger)
	p4p.fp = fp

	var wg sync.WaitGroup

	go func() {
		defer wg.Done()
		logger.Debugf("Starting  LogParser")
		fp.LogParser(ctx, p4p.lines, p4p.cmdchan)
		logger.Debugf("Finished LogParser")
	}()

	go func() {
		defer wg.Done()
		logger.Debugf("Starting to process events")
		result := p4p.ProcessEvents(ctx, lines, metrics)
		logger.Debugf("Finished process events")
		assert.Equal(t, 0, result)
	}()

	for _, l := range eol.Split(input, -1) {
		lines <- []byte(l)
	}

	output := []string{}

	go func() {
		defer wg.Done()
		time.Sleep(20 * time.Millisecond)
		logger.Debugf("Waiting for metrics")
		output = getOutput(metrics)
	}()

	wg.Add(3)
	time.Sleep(50 * time.Millisecond)
	close(lines)
	logger.Debugf("Waiting for finish")
	wg.Wait()
	logger.Debugf("Finished")
	return output
}

func TestP4PromBasic(t *testing.T) {
	cfg := &config.Config{
		ServerID:         "myserverid",
		UpdateInterval:   10 * time.Millisecond,
		OutputCmdsByUser: true}
	input := `
Perforce server info:
	2015/09/02 15:23:09 pid 1616 robert@robert-test 127.0.0.1 [p4/2016.2/LINUX26X86_64/1598668] 'user-sync //...'
Perforce server info:
	2015/09/02 15:23:09 pid 1616 compute end .031s
Perforce server info:
	2015/09/02 15:23:09 pid 1616 completed .031s
`

	output := basicTest(t, cfg, input)

	assert.Equal(t, 7, len(output))
	expected := eol.Split(`p4_cmd_counter{cmd="user-sync",serverid="myserverid"} 1
p4_cmd_cumulative_seconds{cmd="user-sync",serverid="myserverid"} 0.031
p4_cmd_user_counter{user="robert",serverid="myserverid"} 1
p4_cmd_user_cumulative_seconds{user="robert",serverid="myserverid"} 0.031
p4_prom_cmds_pending{serverid="myserverid"} 0
p4_prom_cmds_processed{serverid="myserverid"} 1
p4_prom_log_lines_read{serverid="myserverid"} 8`, -1)
	assert.Equal(t, expected, output)

}

func TestP4PromBasicNoUser(t *testing.T) {
	cfg := &config.Config{
		ServerID:         "myserverid",
		UpdateInterval:   10 * time.Millisecond,
		OutputCmdsByUser: false}

	input := `
Perforce server info:
	2015/09/02 15:23:09 pid 1616 robert@robert-test 127.0.0.1 [p4/2016.2/LINUX26X86_64/1598668] 'user-sync //...'
Perforce server info:
	2015/09/02 15:23:09 pid 1616 compute end .031s
Perforce server info:
	2015/09/02 15:23:09 pid 1616 completed .031s
`

	output := basicTest(t, cfg, input)

	assert.Equal(t, 5, len(output))
	expected := eol.Split(`p4_cmd_counter{cmd="user-sync",serverid="myserverid"} 1
p4_cmd_cumulative_seconds{cmd="user-sync",serverid="myserverid"} 0.031
p4_prom_cmds_pending{serverid="myserverid"} 0
p4_prom_cmds_processed{serverid="myserverid"} 1
p4_prom_log_lines_read{serverid="myserverid"} 8`, -1)
	assert.Equal(t, expected, output)

}

func TestP4PromMultiCmds(t *testing.T) {
	cfg := &config.Config{
		ServerID:         "myserverid",
		UpdateInterval:   10 * time.Millisecond,
		OutputCmdsByUser: true}
	input := `
Perforce server info:
	2017/12/07 15:00:21 pid 148469 fred@LONWS 10.40.16.14/10.40.48.29 [3DSMax/1.0.0.0] 'user-change -i' trigger swarm.changesave
lapse .044s
Perforce server info:
	2017/12/07 15:00:21 pid 148469 completed .413s 7+4us 0+584io 0+0net 4580k 0pf
Perforce server info:
	2017/12/07 15:00:21 pid 148469 fred@LONWS 10.40.16.14/10.40.48.29 [3DSMax/1.0.0.0] 'user-change -i'
--- lapse .413s
--- usage 10+11us 12+13io 14+15net 4088k 22pf
--- rpc msgs/size in+out 20+21/22mb+23mb himarks 318788/318789 snd/rcv .001s/.002s
--- db.counters
---   pages in+out+cached 6+3+2
---   locks read/write 0/2 rows get+pos+scan put+del 2+0+0 1+0

Perforce server info:
	2018/06/10 23:30:08 pid 25568 fred@lon_ws 10.1.2.3 [p4/2016.2/LINUX26X86_64/1598668] 'dm-CommitSubmit'

Perforce server info:
	2018/06/10 23:30:08 pid 25568 fred@lon_ws 10.1.2.3 [p4/2016.2/LINUX26X86_64/1598668] 'dm-CommitSubmit'
--- meta/commit(W)
---   total lock wait+held read/write 0ms+0ms/0ms+795ms

Perforce server info:
	2018/06/10 23:30:08 pid 25568 fred@lon_ws 10.1.2.3 [p4/2016.2/LINUX26X86_64/1598668] 'dm-CommitSubmit'
--- clients/MCM_client_184%2E51%2E33%2E29_prod_prefix1(W)
---   total lock wait+held read/write 0ms+0ms/0ms+1367ms

Perforce server info:
	2018/06/10 23:30:09 pid 25568 completed 1.38s 34+61us 59680+59904io 0+0net 127728k 1pf
Perforce server info:
	2018/06/10 23:30:08 pid 25568 fred@lon_ws 10.1.2.3 [p4/2016.2/LINUX26X86_64/1598668] 'dm-CommitSubmit'
--- db.integed
---   total lock wait+held read/write 12ms+22ms/24ms+795ms
--- db.archmap
---   total lock wait+held read/write 32ms+33ms/34ms+780ms
`

	output := basicTest(t, cfg, input)

	assert.Equal(t, 22, len(output))
	expected := eol.Split(`p4_cmd_counter{cmd="dm-CommitSubmit",serverid="myserverid"} 1
p4_cmd_counter{cmd="user-change",serverid="myserverid"} 1
p4_cmd_cumulative_seconds{cmd="dm-CommitSubmit",serverid="myserverid"} 1.380
p4_cmd_cumulative_seconds{cmd="user-change",serverid="myserverid"} 0.413
p4_cmd_user_counter{user="fred",serverid="myserverid"} 2
p4_cmd_user_cumulative_seconds{user="fred",serverid="myserverid"} 1.793
p4_prom_cmds_pending{serverid="myserverid"} 0
p4_prom_cmds_processed{serverid="myserverid"} 2
p4_prom_log_lines_read{serverid="myserverid"} 37
p4_total_read_held_seconds{table="archmap",serverid="myserverid"} 0.033
p4_total_read_held_seconds{table="counters",serverid="myserverid"} 0.000
p4_total_read_held_seconds{table="integed",serverid="myserverid"} 0.022
p4_total_read_wait_seconds{table="archmap",serverid="myserverid"} 0.032
p4_total_read_wait_seconds{table="counters",serverid="myserverid"} 0.000
p4_total_read_wait_seconds{table="integed",serverid="myserverid"} 0.012
p4_total_trigger_lapse_seconds{trigger="swarm.changesave",serverid="myserverid"} 0.044
p4_total_write_held_seconds{table="archmap",serverid="myserverid"} 0.780
p4_total_write_held_seconds{table="counters",serverid="myserverid"} 0.000
p4_total_write_held_seconds{table="integed",serverid="myserverid"} 0.795
p4_total_write_wait_seconds{table="archmap",serverid="myserverid"} 0.034
p4_total_write_wait_seconds{table="counters",serverid="myserverid"} 0.000
p4_total_write_wait_seconds{table="integed",serverid="myserverid"} 0.024`, -1)
	assert.Equal(t, expected, output)

}

var multiUserIntput = `
Perforce server info:
	2015/09/02 15:23:09 pid 1616 robert@robert-test 127.0.0.1 [p4/2016.2/LINUX26X86_64/1598668] 'user-fstat //some/file'
Perforce server info:
	2015/09/02 15:23:09 pid 1616 completed .011s

Perforce server info:
	2015/09/02 15:23:10 pid 1616 ROBERT@robert-test 127.0.0.1 [p4/2016.2/LINUX26X86_64/1598668] 'user-fstat //some/file'
Perforce server info:
	2015/09/02 15:23:10 pid 1616 completed .011s
`

func TestP4PromBasicMultiUserCaseSensitive(t *testing.T) {
	// Case sensitive/insensitive user
	cfg := &config.Config{
		ServerID:            "myserverid",
		UpdateInterval:      10 * time.Millisecond,
		OutputCmdsByUser:    true,
		CaseSensitiveServer: true}
	output := basicTest(t, cfg, multiUserIntput)
	assert.Equal(t, 9, len(output))
	expected := eol.Split(`p4_cmd_counter{cmd="user-fstat",serverid="myserverid"} 2
p4_cmd_cumulative_seconds{cmd="user-fstat",serverid="myserverid"} 0.022
p4_cmd_user_counter{user="ROBERT",serverid="myserverid"} 1
p4_cmd_user_counter{user="robert",serverid="myserverid"} 1
p4_cmd_user_cumulative_seconds{user="ROBERT",serverid="myserverid"} 0.011
p4_cmd_user_cumulative_seconds{user="robert",serverid="myserverid"} 0.011
p4_prom_cmds_pending{serverid="myserverid"} 0
p4_prom_cmds_processed{serverid="myserverid"} 2
p4_prom_log_lines_read{serverid="myserverid"} 11`, -1)
	assert.Equal(t, expected, output)

}

func TestP4PromBasicMultiUserCaseInsensitive(t *testing.T) {
	// Case sensitive/insensitive user
	cfg := &config.Config{
		ServerID:            "myserverid",
		UpdateInterval:      10 * time.Millisecond,
		OutputCmdsByUser:    true,
		CaseSensitiveServer: false}
	output := basicTest(t, cfg, multiUserIntput)
	assert.Equal(t, 7, len(output))
	expected := eol.Split(`p4_cmd_counter{cmd="user-fstat",serverid="myserverid"} 2
p4_cmd_cumulative_seconds{cmd="user-fstat",serverid="myserverid"} 0.022
p4_cmd_user_counter{user="robert",serverid="myserverid"} 2
p4_cmd_user_cumulative_seconds{user="robert",serverid="myserverid"} 0.022
p4_prom_cmds_pending{serverid="myserverid"} 0
p4_prom_cmds_processed{serverid="myserverid"} 2
p4_prom_log_lines_read{serverid="myserverid"} 11`, -1)
	assert.Equal(t, expected, output)

}
