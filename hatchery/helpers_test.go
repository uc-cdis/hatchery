package hatchery

import "testing"

func TestStrToInt(t *testing.T) {
	testCases := []struct {
		input string
		want  string
	}{
		{"2", "2"},
		{"2.5", "2"},
		{"0.5", "0"},
		{"", ""},
	}

	for _, testcase := range testCases {
		got, err := StrToInt(testcase.input)
		if err != nil {
			t.Errorf("StrToInt(%q) returned unexpected error: %v", testcase.input, err)
		}
		if got != testcase.want {
			t.Errorf("StrToInt(%q) = %q, want %q", testcase.input, got, testcase.want)
		}
	}
}

func TestMem(t *testing.T) {
	testCases := []struct {
		input string
		want  string
	}{
		{"512Mi", "512"},
		{"1Gi", "1024"},
		{"4Gi", "4096"},
		{"2Gib", "2048"},
	}

	for _, testcase := range testCases {
		got, err := mem(testcase.input)
		if err != nil {
			t.Errorf("mem(%q) returned unexpected error: %v", testcase.input, err)
		}
		if got != testcase.want {
			t.Errorf("mem(%q) = %q, want %q", testcase.input, got, testcase.want)
		}
	}
}

func TestCpu(t *testing.T) {
	testCases := []struct {
		input string
		want  string
	}{
		{"1.0", "1024"},
		{"2.0", "2048"},
		{"1.5", "1024"},
	}

	for _, testcase := range testCases {
		got, err := cpu(testcase.input)
		if err != nil {
			t.Errorf("cpu(%q) returned unexpected error: %v", testcase.input, err)
		}
		if got != testcase.want {
			t.Errorf("cpu(%q) = %q, want %q", testcase.input, got, testcase.want)
		}
	}
}

func TestCpuNonNumeric(t *testing.T) {
	if _, err := cpu("abc.5"); err == nil {
		t.Errorf("cpu(%q) should return an error for a non-numeric value", "abc.5")
	}
}

func TestEscapism(t *testing.T) {
	testCases := []struct {
		input string
		want  string
	}{
		{"abc123", "abc123"},
		{"A", "-41"},
		{"user@x.com", "user-40x-2ecom"},
	}

	for _, testcase := range testCases {
		got := escapism(testcase.input)
		if got != testcase.want {
			t.Errorf("escapism(%q) = %q, want %q", testcase.input, got, testcase.want)
		}
	}
}

func TestTruncateString(t *testing.T) {
	testCases := []struct {
		name  string
		input string
		num   int
		want  string
	}{
		{"shorter than limit", "abc", 10, "abc"},
		{"equal to limit", "abcde", 5, "abcde"},
		{"longer than limit", "abcdefgh", 5, "abcde"},
	}

	for _, testcase := range testCases {
		got := truncateString(testcase.input, testcase.num)
		if got != testcase.want {
			t.Errorf("%s: truncateString(%q, %d) = %q, want %q", testcase.name, testcase.input, testcase.num, got, testcase.want)
		}
	}
}
