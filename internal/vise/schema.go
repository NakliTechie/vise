package vise

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
)

// commitPattern matches a full Git object name, which is what GitHead returns
// and therefore the only shape record ever writes into recorded_commit.
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$|^[0-9a-f]{64}$`)

// rejectDuplicateJSONKeys walks the raw lockfile bytes and refuses an object
// that names the same key twice. encoding/json silently keeps the last such
// value, so without this a lockfile could carry one baseline for a reader and
// another for a human, and the tamper hash would cover bytes nobody judged.
func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return scanJSONValue(decoder, "")
}

func scanJSONValue(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key at %s is not a string", displayPath(path))
			}
			if seen[key] {
				return fmt.Errorf("duplicate key %q at %s", key, displayPath(path))
			}
			seen[key] = true
			if err := scanJSONValue(decoder, path+"."+key); err != nil {
				return err
			}
		}
	case '[':
		for index := 0; decoder.More(); index++ {
			if err := scanJSONValue(decoder, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	// Consume the matching closing delimiter.
	_, err = decoder.Token()
	return err
}

func displayPath(path string) string {
	if path == "" {
		return "the top level"
	}
	return path
}

// validateLockfileSchema checks everything about a lockfile that is not a
// content hash: the identifiers name probes and metrics the manifest could
// declare, and every recorded commit is a real Git object name.
func validateLockfileSchema(lock Lockfile) error {
	for id, probe := range lock.Probes {
		if !idPattern.MatchString(id) {
			return fmt.Errorf("probe id %q must match %s", id, idPattern.String())
		}
		if reservedIDs[id] {
			return fmt.Errorf("probe id %q is reserved for harness failures", id)
		}
		if !commitPattern.MatchString(probe.RecordedCommit) {
			return fmt.Errorf("probe %s recorded_commit %q is not a Git object name", id, probe.RecordedCommit)
		}
		for path := range probe.Deps {
			if err := ValidateRelativePath("", path, false); err != nil {
				return fmt.Errorf("probe %s dependency %q: %w", id, path, err)
			}
		}
		for path := range probe.Files {
			if err := ValidateArtifactPath("", path); err != nil {
				return fmt.Errorf("probe %s artifact %q: %w", id, path, err)
			}
		}
	}
	for id := range lock.Metrics {
		if !idPattern.MatchString(id) {
			return fmt.Errorf("metric id %q must match %s", id, idPattern.String())
		}
		if reservedIDs[id] {
			return fmt.Errorf("metric id %q is reserved for harness failures", id)
		}
	}
	return nil
}
