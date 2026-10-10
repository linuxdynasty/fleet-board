package machine

import (
	"errors"
	"io/fs"
	"testing"
)

func TestLevelFromPSI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		avg  float64
		want uint32
	}{
		{"idle", 0, 1},
		{"just under warn", 9.99, 1},
		{"warn starts at 10", 10, 2},
		{"just under critical", 39.99, 2},
		{"critical starts at 40", 40, 4},
		{"saturated", 100, 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := levelFromPSI(tc.avg); got != tc.want {
				t.Fatalf("levelFromPSI(%v) = %d, want %d", tc.avg, got, tc.want)
			}
		})
	}
}

func TestLevelFromAvailable(t *testing.T) {
	t.Parallel()
	const gib = 1 << 30
	tests := []struct {
		name         string
		total, avail uint64
		want         uint32
		wantErr      bool
	}{
		{"plenty", 16 * gib, 8 * gib, 1, false},
		{"exactly 15 percent is normal", 100, 15, 1, false},
		{"under 15 percent warns", 100, 14, 2, false},
		{"exactly 5 percent warns", 100, 5, 2, false},
		{"under 5 percent is critical", 100, 4, 4, false},
		{"nothing available is a real reading", 100, 0, 4, false},
		{"no total is unknown", 0, 0, 0, true},
		{"available over total is unknown", 100, 101, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := levelFromAvailable(tc.total, tc.avail)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("levelFromAvailable(%d, %d) = %d, %v; want %d, err=%v", tc.total, tc.avail, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestParsePSI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		want    float64
		wantErr bool
	}{
		{"kernel format", "some avg10=12.34 avg60=1.00 avg300=0.10 total=123456\nfull avg10=1.00 avg60=0.00 avg300=0.00 total=99\n", 12.34, false},
		{"some is not first", "full avg10=50.00 avg60=0 avg300=0 total=1\nsome avg10=3.00 avg60=0 avg300=0 total=1\n", 3, false},
		{"empty", "", 0, true},
		{"no some line", "full avg10=1.00 avg60=0 avg300=0 total=1\n", 0, true},
		{"no avg10", "some avg60=1.00 total=1\n", 0, true},
		{"not a number", "some avg10=abc avg60=0 avg300=0 total=1\n", 0, true},
		{"negative", "some avg10=-1.00 avg60=0 avg300=0 total=1\n", 0, true},
		{"over 100", "some avg10=100.01 avg60=0 avg300=0 total=1\n", 0, true},
		{"nan", "some avg10=NaN avg60=0 avg300=0 total=1\n", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parsePSI(tc.in)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("parsePSI = %v, %v; want %v, err=%v", got, err, tc.want, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, errParse) {
				t.Fatalf("error %v is not errParse", err)
			}
		})
	}
}

func TestPressureFromPSIFallsBackToMemAvailable(t *testing.T) {
	t.Parallel()
	psi := func(text string, err error) func(string) ([]byte, error) {
		return func(string) ([]byte, error) { return []byte(text), err }
	}
	tests := []struct {
		name    string
		read    func(string) ([]byte, error)
		total   uint64
		avail   uint64
		memOK   bool
		want    uint32
		wantErr bool
	}{
		{"psi wins over available", psi("some avg10=45.00 avg60=0 avg300=0 total=1\n", nil), 100, 90, true, 4, false},
		{"psi without a memory reading", psi("some avg10=11.00 avg60=0 avg300=0 total=1\n", nil), 0, 0, false, 2, false},
		{"no psi file uses available", psi("", fs.ErrNotExist), 100, 10, true, 2, false},
		{"unreadable psi uses available", psi("", fs.ErrPermission), 100, 90, true, 1, false},
		{"garbled psi uses available", psi("junk", nil), 100, 3, true, 4, false},
		{"no psi and no memory reading is unknown", psi("", fs.ErrNotExist), 0, 0, false, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := pressureFromPSI(tc.read, tc.total, tc.avail, tc.memOK)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("pressureFromPSI = %d, %v; want %d, err=%v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}
