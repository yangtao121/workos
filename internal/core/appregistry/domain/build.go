package domain

import (
	"bytes"
	"encoding/json"
	"path"
	"strings"
	"unicode/utf8"
)

// BuildOutput is the frozen-bundle recipe (ADR-0033). Directory is a
// normalized relative path; Format is the only allowed encoding.
type BuildOutput struct {
	Format    string `json:"format"`
	Directory string `json:"directory"`
}

// BuildRecipe is the execution projection of the canonical manifest schema,
// never a second manifest format. Source and commands are fixed by its digest.
type BuildRecipe struct {
	SourceBundleID string       `json:"sourceBundleId"`
	SourceDigest   string       `json:"sourceDigest"`
	BaseImage      string       `json:"baseImage"`
	BuildCommand   []string     `json:"buildCommand"`
	TestCommand    []string     `json:"testCommand"`
	Output         *BuildOutput `json:"output,omitempty"`
}

func ParseBuildRecipe(canonical []byte) (*BuildRecipe, bool) {
	var document map[string]json.RawMessage
	if json.Unmarshal(canonical, &document) != nil {
		return nil, false
	}
	raw, present := document["build"]
	if !present {
		return nil, true
	}
	var runtime struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(document["runtime"], &runtime) != nil || runtime.Type != RuntimeTypeContainer {
		return nil, false
	}
	var recipe *BuildRecipe
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&recipe) != nil || recipe == nil || !ValidSourceID(recipe.SourceBundleID) || !ValidWebBundleArtifactDigest(recipe.SourceDigest) || !ValidContainerImage(recipe.BaseImage) || !ValidContainerCommand(recipe.BuildCommand) || !ValidContainerCommand(recipe.TestCommand) {
		return nil, false
	}
	if recipe.Output != nil {
		if recipe.Output.Format != "app-bundle.v1" || !ValidBuildOutputDirectory(recipe.Output.Directory) {
			return nil, false
		}
	}
	return recipe, true
}

// ValidBuildOutputDirectory matches the Schema grammar for build.output.directory.
func ValidBuildOutputDirectory(value string) bool {
	if value == "" || utf8.RuneCountInString(value) > 64 || value != path.Clean(value) || path.IsAbs(value) {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		tokens := strings.Split(segment, ".")
		if len(tokens) == 0 {
			return false
		}
		for _, token := range tokens {
			if token == "" {
				return false
			}
			for _, r := range token {
				switch {
				case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
				default:
					return false
				}
			}
		}
	}
	return true
}
