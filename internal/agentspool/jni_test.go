package agentspool

import "testing"

// TestHookSymbol: the JNI short names of the hook table's natives, own
// class and qualified, and the escapes JNI defines for names that need
// them.
func TestHookSymbol(t *testing.T) {
	cases := []struct {
		hook Hook
		want string
	}{
		{Hook{Class: "java/io/FileInputStream", Native: "readBytes"}, "Java_java_io_FileInputStream_readBytes"},
		{Hook{Class: "java/io/FileInputStream", Native: "open0"}, "Java_java_io_FileInputStream_open0"},
		{Hook{Class: "sun/nio/ch/NioSocketImpl", Native: "sun/nio/ch/Net.accept"}, "Java_sun_nio_ch_Net_accept"},
		{Hook{Class: "sun/nio/ch/SocketDispatcher", Native: "sun/nio/ch/FileDispatcherImpl.write0"},
			"Java_sun_nio_ch_FileDispatcherImpl_write0"},
		{Hook{Class: "a/b_c/D$E", Native: "f_g"}, "Java_a_b_1c_D_00024E_f_1g"},
		{Hook{Class: "a/Ä", Native: "x"}, "Java_a__000c4_x"},
		{Hook{Class: "a/B", Native: "\U0001F600"}, "Java_a_B__0d83d_0de00"},
	}
	for _, c := range cases {
		if got := c.hook.Symbol(); got != c.want {
			t.Errorf("%s.%s: symbol %q, want %q", c.hook.Class, c.hook.Native, got, c.want)
		}
	}
}
