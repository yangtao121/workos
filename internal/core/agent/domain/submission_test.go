package domain

import "testing"

func TestSubmissionIdentityPreservesExactInputAndIgnoresJSONLayout(t *testing.T) {
	task := Task{OwnerUserID: "owner", ProjectID: "project", ProviderID: "old-provider", Input: []byte(`{"goal":"repair","budget":{"maxTokens":9007199254740992},"contextRefs":["a","b"]}`)}
	for _, tc := range []struct {
		name, owner, project, input string
		want                        bool
	}{
		{"layout", "owner", "project", `{ "contextRefs": ["a", "b"], "budget": {"maxTokens":9007199254740992}, "goal":"repair" }`, true},
		{"owner", "other", "project", string(task.Input), false},
		{"project", "owner", "other", string(task.Input), false},
		{"goal", "owner", "project", `{"goal":"other","budget":{"maxTokens":9007199254740992},"contextRefs":["a","b"]}`, false},
		{"exact integer", "owner", "project", `{"goal":"repair","budget":{"maxTokens":9007199254740993},"contextRefs":["a","b"]}`, false},
		{"ordered refs", "owner", "project", `{"goal":"repair","budget":{"maxTokens":9007199254740992},"contextRefs":["b","a"]}`, false},
		{"trailing JSON", "owner", "project", string(task.Input) + `{}`, false},
		{"null", "owner", "project", `null`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := task.MatchesSubmission(tc.owner, tc.project, []byte(tc.input)); got != tc.want {
				t.Fatalf("match=%v want=%v", got, tc.want)
			}
		})
	}
	task.Input = []byte(`null`)
	if task.MatchesSubmission("owner", "project", []byte(`null`)) {
		t.Fatal("corrupt stored input matched")
	}
}
