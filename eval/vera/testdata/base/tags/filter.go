package tags

import "encoding/json"

// Matches reports whether a memory's tags_json carries every wanted tag.
// Recall uses it to drop candidates that do not match the caller's filter.
func Matches(tagsJSON []byte, want map[string]string) bool {
	if len(want) == 0 {
		return true
	}
	var have map[string]string
	if len(tagsJSON) == 0 || json.Unmarshal(tagsJSON, &have) != nil {
		return false
	}
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}
