// Package manifestvalidator adapts untrusted YAML manifest bytes to a
// validated domain.Manifest. It enforces the canonical JSON Schema embedded
// at schemas/workos-app-manifest-v1.schema.json plus the structural YAML and
// semantic security rules that are not expressible as JSON shape. The schema
// file is the single rule source: no second copy of manifest rules lives in
// Go structs, Proto, or TypeScript.
package manifestvalidator

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"gopkg.in/yaml.v3"

	"github.com/yangtao121/workos/internal/core/appregistry/domain"
	"github.com/yangtao121/workos/schemas"
)

const (
	// maxStructureNodes and maxStructureDepth bound YAML tree walking.
	maxStructureNodes = 20000
	maxStructureDepth = 32

	maxViolations     = 32
	maxViolationRunes = 256
)

// Validator is immutable after New and safe for concurrent use.
type Validator struct {
	schema *jsonschema.Schema
}

// denyLoader refuses every URL: the canonical schema resolves fully from the
// embedded resource, and any (future) `$ref` must fail closed instead of
// reaching the network.
type denyLoader struct{}

func (denyLoader) Load(string) (any, error) {
	return nil, fmt.Errorf("external schema references are not allowed")
}

// New compiles the embedded canonical schema. Compilation failure is a
// startup error: the registry must not accept manifests without the schema.
func New() (*Validator, error) {
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemas.AppManifestV1))
	if err != nil {
		return nil, fmt.Errorf("parse canonical app manifest schema: %w", err)
	}
	resourceURL, ok := documentID(document)
	if !ok {
		return nil, fmt.Errorf("canonical app manifest schema has no $id")
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(denyLoader{})
	if err := compiler.AddResource(resourceURL, document); err != nil {
		return nil, fmt.Errorf("register canonical app manifest schema: %w", err)
	}
	compiled, err := compiler.Compile(resourceURL)
	if err != nil {
		return nil, fmt.Errorf("compile canonical app manifest schema: %w", err)
	}
	return &Validator{schema: compiled}, nil
}

func documentID(document any) (string, bool) {
	mapping, ok := document.(map[string]any)
	if !ok {
		return "", false
	}
	id, ok := mapping["$id"].(string)
	return id, ok && id != ""
}

// Validate runs the full pipeline. It returns the validated manifest when the
// input is acceptable, and otherwise a deterministic, deduplicated, bounded
// list of violations that contain field paths and rule descriptions only —
// never raw YAML values or internal stack details. All rule failures are
// violations; the validator itself performs no I/O and has no internal error
// path beyond New.
func (v *Validator) Validate(yamlBytes []byte) (domain.Manifest, []string) {
	var violations []string
	add := func(path, message string) {
		violations = append(violations, formatViolation(path, message))
	}

	if len(yamlBytes) > domain.MaxManifestBytes {
		add("", fmt.Sprintf("manifest exceeds the %d byte limit", domain.MaxManifestBytes))
		return domain.Manifest{}, finalizeViolations(violations)
	}

	tree, ok := v.structure(yamlBytes, add)
	if !ok {
		return domain.Manifest{}, finalizeViolations(violations)
	}

	canonical, err := domain.CanonicalJSON(tree)
	if err != nil {
		add("", "manifest values cannot be represented canonically")
		return domain.Manifest{}, finalizeViolations(violations)
	}

	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(canonical))
	if err != nil {
		add("", "manifest values are not valid JSON values")
		return domain.Manifest{}, finalizeViolations(violations)
	}
	if err := v.schema.Validate(instance); err != nil {
		validation, ok := err.(*jsonschema.ValidationError)
		if !ok {
			add("", "manifest does not satisfy the canonical schema")
		} else {
			collectSchemaViolations(validation, add)
		}
	}

	v.policy(tree, add)

	if len(violations) > 0 {
		return domain.Manifest{}, finalizeViolations(violations)
	}

	sortPermissions(tree)
	finalCanonical, err := domain.CanonicalJSON(tree)
	if err != nil {
		add("", "manifest values cannot be represented canonically")
		return domain.Manifest{}, finalizeViolations(violations)
	}
	return manifestFromTree(tree, finalCanonical), nil
}

// structure decodes YAML under the structural safety rules and converts it to
// a JSON-compatible value tree. It reports whether a usable tree exists; on
// failure the appended violations describe the first structural problems.
func (v *Validator) structure(yamlBytes []byte, add func(path, message string)) (map[string]any, bool) {
	reader := bytes.NewReader(yamlBytes)
	decoder := yaml.NewDecoder(reader)
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		if typed, ok := err.(*yaml.TypeError); ok && len(typed.Errors) > 0 {
			add("", "manifest is not valid YAML: syntax error")
		} else if err == io.EOF {
			add("", "manifest is empty")
		} else {
			add("", "manifest is not valid YAML")
		}
		return nil, false
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		add("", "manifest must contain exactly one YAML document")
		return nil, false
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) == 0 {
		add("", "manifest root must be a YAML mapping")
		return nil, false
	}
	walker := &structureWalker{add: add}
	root := walker.value(document.Content[0], "", 0)
	if walker.failed || root == nil {
		return nil, false
	}
	mapping, ok := root.(map[string]any)
	if !ok {
		add("", "manifest root must be a YAML mapping")
		return nil, false
	}
	return mapping, true
}

type structureWalker struct {
	add    func(path, message string)
	nodes  int
	failed bool
}

func (w *structureWalker) value(node *yaml.Node, path string, depth int) any {
	if w.failed {
		return nil
	}
	w.nodes++
	if w.nodes > maxStructureNodes {
		w.add("", "manifest structure is too large")
		w.failed = true
		return nil
	}
	if depth > maxStructureDepth {
		w.add(path, "manifest nesting exceeds the allowed depth")
		w.failed = true
		return nil
	}
	if node.Anchor != "" {
		w.add(path, "YAML anchors are not allowed")
		w.failed = true
		return nil
	}
	switch node.Kind {
	case yaml.AliasNode:
		w.add(path, "YAML aliases are not allowed")
		w.failed = true
		return nil
	case yaml.MappingNode:
		return w.mapping(node, path, depth)
	case yaml.SequenceNode:
		if node.Tag != "" && node.Tag != "!!seq" {
			w.reportTag(path)
			return nil
		}
		items := make([]any, 0, len(node.Content))
		for index, child := range node.Content {
			items = append(items, w.value(child, path+"/"+strconv.Itoa(index), depth+1))
			if w.failed {
				return nil
			}
		}
		return items
	case yaml.ScalarNode:
		return w.scalar(node, path)
	default:
		w.add(path, "unsupported YAML construct")
		w.failed = true
		return nil
	}
}

func (w *structureWalker) mapping(node *yaml.Node, path string, depth int) map[string]any {
	if node.Tag != "" && node.Tag != "!!map" {
		w.reportTag(path)
		return nil
	}
	result := make(map[string]any, len(node.Content)/2)
	for index := 0; index+1 < len(node.Content); index += 2 {
		keyNode, valueNode := node.Content[index], node.Content[index+1]
		if keyNode.Tag == "!!merge" || keyNode.Value == "<<" {
			w.add(path, "YAML merge keys are not allowed")
			w.failed = true
			return nil
		}
		if keyNode.Kind != yaml.ScalarNode || keyNode.Tag != "!!str" {
			w.add(path, "mapping keys must be strings")
			w.failed = true
			return nil
		}
		key := keyNode.Value
		// Keys are validated before any pointer construction, map insertion,
		// schema validation, or persistence, and unsafe keys are reported by
		// parent path only so the raw key never reaches a violation message.
		if !validMappingKey(key) {
			w.add(path, "mapping keys must be valid UTF-8, control-free, and between 1 and 256 characters")
			w.failed = true
			return nil
		}
		// The key itself may carry credential material (a prefixed token, JWT,
		// AWS key ID, or PEM header used as a key). The shared credential-shape
		// rule rejects it here — before any pointer construction, canonical
		// encoding, or persistence — and reports only the parent path, never
		// the key itself.
		if credentialShapedString(key) {
			w.add(path, "mapping keys that look like credentials are not allowed in manifests")
			w.failed = true
			return nil
		}
		if _, duplicate := result[key]; duplicate {
			w.add(pointerChild(path, key), "duplicate mapping key")
			w.failed = true
			return nil
		}
		result[key] = w.value(valueNode, pointerChild(path, key), depth+1)
		if w.failed {
			return nil
		}
	}
	return result
}

// maxMappingKeyRunes bounds each mapping key so free-form blocks cannot use
// pathological key lengths.
const maxMappingKeyRunes = 256

func validMappingKey(key string) bool {
	if !utf8.ValidString(key) {
		return false
	}
	count := 0
	for _, r := range key {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return false
		}
		count++
		if count > maxMappingKeyRunes {
			return false
		}
	}
	return count > 0
}

func (w *structureWalker) scalar(node *yaml.Node, path string) any {
	switch node.Tag {
	case "!!str":
		return node.Value
	case "!!null":
		return nil
	case "!!bool":
		return node.Value == "true"
	case "!!int":
		number, err := strconv.ParseInt(node.Value, 10, 64)
		if err != nil {
			w.add(path, "integers must be decimal and fit in 64 bits")
			w.failed = true
			return nil
		}
		return number
	case "!!float":
		number, err := strconv.ParseFloat(node.Value, 64)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
			w.add(path, "numbers must be finite decimal values")
			w.failed = true
			return nil
		}
		return number
	case "!!timestamp", "!!binary":
		w.add(path, fmt.Sprintf("YAML %s values are not allowed", strings.TrimPrefix(node.Tag, "!!")))
		w.failed = true
		return nil
	default:
		w.add(path, "custom YAML tags are not allowed")
		w.failed = true
		return nil
	}
}

func (w *structureWalker) reportTag(path string) {
	w.add(path, "unsupported YAML collection")
	w.failed = true
}

// policy applies the semantic security rules that are outside JSON shape:
// public trust boundary, capability vocabulary, and secret-bearing content.
// It only inspects values that decode to the expected shape.
func (v *Validator) policy(tree map[string]any, add func(path, message string)) {
	if scope, ok := tree["scope"].(string); ok && domain.Scope(scope) == domain.ScopeSystem {
		add("/scope", "scope 'system' requires a trusted installation path and cannot be self-registered")
	}
	if runtime, ok := tree["runtime"].(map[string]any); ok {
		if runtimeType, ok := runtime["type"].(string); ok {
			switch runtimeType {
			case "trusted":
				add("/runtime/type", "runtime type 'trusted' requires a trusted installation path and cannot be self-registered")
			case domain.RuntimeTypeWebBundle:
				webBundlePolicy(runtime, tree, add)
			case domain.RuntimeTypeContainer:
				containerPolicy(runtime, tree, add)
			}
		}
	}
	if version, ok := tree["version"].(string); ok {
		if _, parseable := domain.ParseVersion(version); !parseable {
			add("/version", "version must be a semantic version with non-empty prerelease identifiers")
		}
	}
	if permissions, ok := tree["permissions"].([]any); ok {
		for index, item := range permissions {
			capability, ok := item.(string)
			if !ok {
				continue
			}
			if !domain.KnownPermission(capability) {
				add("/permissions/"+strconv.Itoa(index), "unknown capability; permissions must use the published capability vocabulary")
			}
		}
	}
	scanSecrets(tree, "", add)
	scanStrings(tree, "", add)
}

// webBundlePolicy enforces the cross-field rules of the additive web-bundle
// launch descriptor: the exact immutable artifact reference is required,
// container-style runtime fields are rejected, and exactly one supported
// web-bundle surface may be declared (this slice has a single deterministic
// entry surface).
func webBundlePolicy(runtime map[string]any, tree map[string]any, add func(path, message string)) {
	artifactID, hasID := runtime["artifactId"].(string)
	artifactDigest, hasDigest := runtime["artifactDigest"].(string)
	if !hasID || !domain.ValidWebBundleArtifactID(artifactID) {
		add("/runtime/artifactId", "runtime type 'web-bundle' requires a valid UUIDv7 artifactId")
	}
	if !hasDigest || !domain.ValidWebBundleArtifactDigest(artifactDigest) {
		add("/runtime/artifactDigest", "runtime type 'web-bundle' requires a sha256 artifactDigest")
	}
	for _, field := range []string{"image", "command", "port"} {
		if _, present := runtime[field]; present {
			add("/runtime/"+field, "runtime type 'web-bundle' does not allow container runtime fields")
		}
	}
	surfaces, ok := tree["surfaces"].([]any)
	if !ok || len(surfaces) != 1 {
		add("/surfaces", "runtime type 'web-bundle' requires exactly one surface")
		return
	}
	surface, ok := surfaces[0].(map[string]any)
	if !ok {
		add("/surfaces/0", "runtime type 'web-bundle' requires exactly one surface")
		return
	}
	if renderer, _ := surface["renderer"].(string); renderer != "web-bundle" {
		add("/surfaces/0/renderer", "runtime type 'web-bundle' only supports the 'web-bundle' renderer")
	}
}

// containerPolicy enforces the cross-field rules of the strict container
// launch profile (ADR-0006): the exact digest-pinned image reference and
// bounded argv are required, web-bundle artifact fields are rejected, exactly
// one web-service surface with the fixed root route may be declared, and the
// requested resource/health policies must carry the canonical keys, integer
// or decimal grammar, and limits. Requests are clamped later by the runtime's
// server-owned maxima; the manifest side only fixes the vocabulary and shape
// so an App cannot declare unbounded or unknown policy fields.
func containerPolicy(runtime map[string]any, tree map[string]any, add func(path, message string)) {
	image, hasImage := runtime["image"].(string)
	if !hasImage || !domain.ValidContainerImage(image) {
		add("/runtime/image", "runtime type 'container' requires an exact image reference pinned by a lowercase sha256 digest with no tag")
	}
	command, hasCommand := runtime["command"].([]any)
	if !hasCommand {
		add("/runtime/command", "runtime type 'container' requires a non-empty argv array")
	} else {
		arguments := make([]string, 0, len(command))
		text := true
		for _, item := range command {
			argument, ok := item.(string)
			if !ok {
				text = false
				break
			}
			arguments = append(arguments, argument)
		}
		if !text || !domain.ValidContainerCommand(arguments) {
			add("/runtime/command", "runtime type 'container' requires a bounded non-empty argv array of control-free strings")
		}
	}
	port, hasPort := runtime["port"]
	if !hasPort {
		add("/runtime/port", "runtime type 'container' requires a container port")
	} else if _, ok := port.(int64); !ok {
		add("/runtime/port", "runtime port must be an integer between 1 and 65535")
	}
	for _, field := range []string{"artifactId", "artifactDigest"} {
		if _, present := runtime[field]; present {
			add("/runtime/"+field, "runtime type 'container' does not allow web bundle artifact fields")
		}
	}
	containerResourcePolicy(runtimeResources(tree), add)
	containerHealthPolicy(treeHealth(tree), add)
	containerSurfacePolicy(tree, add)
}

func runtimeResources(tree map[string]any) map[string]any {
	resources, _ := tree["resources"].(map[string]any)
	return resources
}

func treeHealth(tree map[string]any) map[string]any {
	health, _ := tree["health"].(map[string]any)
	return health
}

func containerResourcePolicy(resources map[string]any, add func(path, message string)) {
	cpuValue, hasCPU := resources["cpuHard"]
	highValue, hasHigh := resources["memoryHighMb"]
	maxValue, hasMax := resources["memoryMaxMb"]
	pidsValue, hasPids := resources["pidsMax"]
	for _, key := range []string{"cpuSoft", "memoryExpectedMb", "cpuExpected", "gpu"} {
		if _, present := resources[key]; present {
			add("/resources/"+key, "resources must use the canonical container policy fields")
		}
	}
	cpu, cpuOK := policyFloat(cpuValue)
	if !hasCPU || !cpuOK || cpu < domain.MinCPUHardCores || cpu > domain.MaxCPUHardCores {
		add("/resources/cpuHard", "resources require a finite cpuHard within the canonical bounds")
	}
	high, highOK := policyInteger(highValue)
	maximum, maxOK := policyInteger(maxValue)
	if !hasHigh || !highOK || high < domain.MinMemoryHighMB || high > domain.MaxMemoryHighMB {
		add("/resources/memoryHighMb", "resources require an integer memoryHighMb within the canonical bounds")
	}
	if !hasMax || !maxOK || maximum < domain.MinMemoryMaxMB || maximum > domain.MaxMemoryMaxMB {
		add("/resources/memoryMaxMb", "resources require an integer memoryMaxMb within the canonical bounds")
	}
	if highOK && maxOK && high > maximum {
		add("/resources/memoryHighMb", "memoryHighMb must not exceed memoryMaxMb")
	}
	pids, pidsOK := policyInteger(pidsValue)
	if !hasPids || !pidsOK || pids < domain.MinPidsMax || pids > domain.MaxPidsMax {
		add("/resources/pidsMax", "resources require an integer pidsMax within the canonical bounds")
	}
	if err := extraKeys(resources, map[string]bool{"cpuHard": true, "memoryHighMb": true, "memoryMaxMb": true, "pidsMax": true}); err != "" {
		add("/resources", err)
	}
}

func containerHealthPolicy(health map[string]any, add func(path, message string)) {
	pathValue, hasPath := health["httpPath"]
	startupValue, hasStartup := health["startupSeconds"]
	limitValue, hasLimit := health["restartLimit"]
	httpPath, pathOK := pathValue.(string)
	if !hasPath || !pathOK ||
		len(httpPath) == 0 || len(httpPath) > 120 || httpPath[0] != '/' {
		add("/health/httpPath", "health requires a bounded absolute httpPath")
	} else {
		for index := 0; index < len(httpPath); index++ {
			c := httpPath[index]
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') &&
				c != '/' && c != '.' && c != '_' && c != '-' {
				add("/health/httpPath", "health httpPath must use plain unreserved path characters")
				break
			}
		}
	}
	startup, startupOK := policyInteger(startupValue)
	if !hasStartup || !startupOK || startup < domain.MinStartupSeconds || startup > domain.MaxStartupSeconds {
		add("/health/startupSeconds", "health requires an integer startupSeconds within the canonical bounds")
	}
	limit, limitOK := policyInteger(limitValue)
	if !hasLimit || !limitOK || limit < domain.MinRestartLimit || limit > domain.MaxRestartLimit {
		add("/health/restartLimit", "health requires an integer restartLimit within the canonical bounds")
	}
	if err := extraKeys(health, map[string]bool{"httpPath": true, "startupSeconds": true, "restartLimit": true}); err != "" {
		add("/health", err)
	}
}

func containerSurfacePolicy(tree map[string]any, add func(path, message string)) {
	surfaces, ok := tree["surfaces"].([]any)
	if !ok || len(surfaces) != 1 {
		add("/surfaces", "runtime type 'container' requires exactly one surface")
		return
	}
	surface, ok := surfaces[0].(map[string]any)
	if !ok {
		add("/surfaces/0", "runtime type 'container' requires exactly one surface")
		return
	}
	if renderer, _ := surface["renderer"].(string); renderer != "web-service" {
		add("/surfaces/0/renderer", "runtime type 'container' only supports the 'web-service' renderer")
	}
	if route, _ := surface["route"].(string); route != "/" {
		add("/surfaces/0/route", "runtime type 'container' fixes the web-service surface route to '/'")
	}
}

// extraKeys returns a fixed message naming the policy object (never values)
// when unknown keys are present; the runner can never silently ignore them.
func extraKeys(object map[string]any, allowed map[string]bool) string {
	for key := range object {
		if !allowed[key] {
			return "unknown policy fields are not allowed"
		}
	}
	return ""
}

// policyFloat accepts int64 or float64 trees (YAML decimals and integers both
// express a decimal limit) and reports the finite value.
func policyFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case int64:
		return float64(typed), true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0, false
		}
		return typed, true
	default:
		return 0, false
	}
}

// policyInteger accepts only true integers: the canonical expression of every
// integral policy quantity. A YAML float (64.0) is rejected outright.
func policyInteger(value any) (int64, bool) {
	number, ok := value.(int64)
	return number, ok
}

func scanSecrets(value any, path string, add func(path, message string)) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			childPath := pointerChild(path, key)
			if secretBearingKey(key) {
				add(childPath, "field names that hold secrets are not allowed in manifests")
			}
			scanSecrets(child, childPath, add)
		}
	case []any:
		for index, item := range typed {
			scanSecrets(item, path+"/"+strconv.Itoa(index), add)
		}
	case string:
		if credentialShapedString(typed) {
			add(path, "values that look like credentials are not allowed in manifests")
		}
	}
}

func scanStrings(value any, path string, add func(path, message string)) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			scanStrings(child, pointerChild(path, key), add)
		}
	case []any:
		for index, item := range typed {
			scanStrings(item, path+"/"+strconv.Itoa(index), add)
		}
	case string:
		if !utf8.ValidString(typed) {
			add(path, "strings must be valid UTF-8")
			return
		}
		for _, r := range typed {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				add(path, "strings must not contain control characters")
				return
			}
		}
		if path == "/name" && strings.TrimFunc(typed, unicode.IsSpace) != typed {
			add(path, "name must not begin or end with whitespace")
		}
	}
}

// sortPermissions canonicalizes the schema-declared set (uniqueItems) to a
// stable order before digesting, so that permission order alone never changes
// the manifest digest.
func sortPermissions(tree map[string]any) {
	permissions, ok := tree["permissions"].([]any)
	if !ok {
		return
	}
	values := make([]string, 0, len(permissions))
	for _, item := range permissions {
		if capability, ok := item.(string); ok {
			values = append(values, capability)
		}
	}
	sort.Strings(values)
	sorted := make([]any, len(values))
	for index, capability := range values {
		sorted[index] = capability
	}
	tree["permissions"] = sorted
}

func manifestFromTree(tree map[string]any, canonical []byte) domain.Manifest {
	manifest := domain.Manifest{CanonicalJSON: canonical, Digest: domain.ManifestDigest(canonical)}
	if id, ok := tree["id"].(string); ok {
		manifest.ID = id
	}
	if name, ok := tree["name"].(string); ok {
		manifest.Name = name
	}
	if version, ok := tree["version"].(string); ok {
		manifest.Version = version
	}
	if scope, ok := tree["scope"].(string); ok {
		manifest.Scope = domain.Scope(scope)
	}
	if permissions, ok := tree["permissions"].([]any); ok {
		manifest.Permissions = make([]string, 0, len(permissions))
		for _, item := range permissions {
			if capability, ok := item.(string); ok {
				manifest.Permissions = append(manifest.Permissions, capability)
			}
		}
	}
	if runtime, ok := tree["runtime"].(map[string]any); ok {
		if runtimeType, ok := runtime["type"].(string); ok {
			manifest.RuntimeType = runtimeType
		}
		switch runtimeType, _ := runtime["type"].(string); runtimeType {
		case domain.RuntimeTypeWebBundle:
			if ref, ok := domain.ParseWebBundleRef(canonical); ok {
				manifest.WebBundle = &ref
			}
		case domain.RuntimeTypeContainer:
			if launch, ok := domain.ParseContainerLaunch(canonical); ok {
				manifest.Container = &launch
			}
		}
	}
	return manifest
}

// collectSchemaViolations flattens the validation error tree into safe,
// deterministic rule descriptions. Kind messages from the library are never
// used verbatim because several of them echo instance values.
func collectSchemaViolations(validation *jsonschema.ValidationError, add func(path, message string)) {
	path := "/" + strings.Join(escapeSegments(validation.InstanceLocation), "/")
	if _, isRoot := validation.ErrorKind.(*kind.Schema); !isRoot {
		if message, ok := describeKind(validation.ErrorKind); ok {
			add(path, message)
		}
	}
	for _, cause := range validation.Causes {
		collectSchemaViolations(cause, add)
	}
}

func describeKind(errorKind jsonschema.ErrorKind) (string, bool) {
	switch typed := errorKind.(type) {
	case *kind.Type:
		return fmt.Sprintf("expected %s", strings.Join(typed.Want, " or ")), true
	case *kind.Required:
		return fmt.Sprintf("missing required property %s", quoteList(typed.Missing)), true
	case *kind.AdditionalProperties:
		return "properties beyond the schema are not allowed", true
	case *kind.Pattern:
		return fmt.Sprintf("must match pattern %s", strconv.Quote(typed.Want)), true
	case *kind.MinLength:
		return fmt.Sprintf("length must be at least %d", typed.Want), true
	case *kind.MaxLength:
		return fmt.Sprintf("length must be at most %d", typed.Want), true
	case *kind.MinItems:
		return fmt.Sprintf("must contain at least %d items", typed.Want), true
	case *kind.MaxItems:
		return fmt.Sprintf("must contain at most %d items", typed.Want), true
	case *kind.Enum:
		return "value is not one of the allowed enum values", true
	case *kind.Const:
		return "value does not equal the required constant", true
	case *kind.UniqueItems:
		return "array items must be unique", true
	default:
		return "failed schema validation", true
	}
}

func quoteList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, strconv.Quote(value))
	}
	return strings.Join(quoted, ", ")
}

var pointerEscaper = strings.NewReplacer("~", "~0", "/", "~1")

func pointerChild(path, key string) string {
	if path == "" {
		return "/" + pointerEscaper.Replace(key)
	}
	return path + "/" + pointerEscaper.Replace(key)
}

func escapeSegments(segments []string) []string {
	escaped := make([]string, 0, len(segments))
	for _, segment := range segments {
		escaped = append(escaped, pointerEscaper.Replace(segment))
	}
	return escaped
}

func formatViolation(path, message string) string {
	if path == "" || path == "/" {
		return message
	}
	return path + ": " + message
}

func finalizeViolations(violations []string) []string {
	if len(violations) == 0 {
		return []string{"manifest is invalid"}
	}
	sorted := make([]string, len(violations))
	copy(sorted, violations)
	sort.Strings(sorted)
	unique := make([]string, 0, len(sorted))
	for _, violation := range sorted {
		if len(unique) > 0 && unique[len(unique)-1] == violation {
			continue
		}
		unique = append(unique, violation)
	}
	if len(unique) > maxViolations {
		suppressed := len(unique) - maxViolations + 1
		unique = append(unique[:maxViolations-1], fmt.Sprintf("(%d more violations suppressed)", suppressed))
	}
	for index, violation := range unique {
		unique[index] = truncateRunes(violation, maxViolationRunes)
	}
	return unique
}

func truncateRunes(value string, limit int) string {
	if len([]rune(value)) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}
