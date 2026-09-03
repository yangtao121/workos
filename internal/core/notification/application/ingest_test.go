package application

import "testing"

func TestValidAppTextRejectsControlsBeforeNormalization(t *testing.T) {
	cases := []struct {
		title string
		body  string
	}{
		{title: "title\u0085", body: "body"},
		{title: "title", body: "body\r\nsecond"},
		{title: "title", body: "body\u009f"},
	}
	for _, tc := range cases {
		if _, _, err := ValidAppText(tc.title, tc.body); err == nil {
			t.Fatalf("accepted control-bearing text title=%q body=%q", tc.title, tc.body)
		}
	}

	lines := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14\n15\n16\n17"
	if _, _, err := ValidAppText("title", lines); err == nil {
		t.Fatal("accepted body above the line bound")
	}
}
