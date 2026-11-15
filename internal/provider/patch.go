package provider

import (
	"context"
	"encoding/json"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

// mergePatch sends a v2 JSON merge-patch request. body is the typed request
// with its list fields left nil; lists holds them instead.
//
// The client models list fields of update requests as UpdateFieldListString,
// which it encodes as {"defined":true,"value":[...]}, but SonarQube expects a
// plain JSON array and answers "Invalid request format" otherwise. Until the
// client encodes them correctly, the lists are added to the body here.
func mergePatch(ctx context.Context, c *sonarqube.Client, path string, params map[string]string, body any, lists map[string][]string, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	for k, v := range lists {
		m[k] = append([]string{}, v...) // never null: an empty list clears the field
	}
	_, err = c.Do(ctx, &sonarqube.Request{Method: "PATCH", Path: path, PathParams: params, JSON: m,
		ContentType: "application/merge-patch+json"}, out)
	return err
}
