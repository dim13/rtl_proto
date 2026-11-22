package rtlprotov1

import "testing"

// These values go on the rtl_tcp wire.
func TestTunerTypeMatchesRTLTCP(t *testing.T) {
	for _, tc := range []struct {
		typ  TunerType
		want int32
	}{
		{typ: TunerType_TUNER_TYPE_R820T, want: 5},
		{typ: TunerType_TUNER_TYPE_R828D, want: 6},
	} {
		if got := int32(tc.typ); got != tc.want {
			t.Errorf("%v: got %d, want %d", tc.typ, got, tc.want)
		}
	}
}
