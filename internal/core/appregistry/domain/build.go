package domain

import (
	"bytes"
	"encoding/json"
)

// BuildRecipe is the execution projection of the canonical manifest schema,
// never a second manifest format. Source and commands are fixed by its digest.
type BuildRecipe struct {
	SourceBundleID string   `json:"sourceBundleId"`
	SourceDigest   string   `json:"sourceDigest"`
	BaseImage      string   `json:"baseImage"`
	BuildCommand   []string `json:"buildCommand"`
	TestCommand    []string `json:"testCommand"`
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
	return recipe, true
}
