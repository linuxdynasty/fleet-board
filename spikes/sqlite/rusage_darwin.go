package main

/*
#include <libproc.h>
#include <sys/resource.h>
#include <unistd.h>
static unsigned long long written(void) {
	struct rusage_info_v2 ri;
	if (proc_pid_rusage(getpid(), RUSAGE_INFO_V2, (rusage_info_t *)&ri) != 0) return 0;
	return ri.ri_diskio_byteswritten;
}
*/
import "C"

// diskWritten is the bytes this process caused to be written to disk
// (macOS rusage ri_diskio_byteswritten), which counts WAL, checkpoints and
// file growth together. cgo is used only by the spike harness, not by the
// drivers under test.
func diskWritten() int64 { return int64(C.written()) }
