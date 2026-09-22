package config

import "github.com/charmbracelet/crush/internal/csync"

type testEnv map[string]string

func (e testEnv) Get(key string) string { return e[key] }
func (e testEnv) Env() []string {
	values := make([]string, 0, len(e))
	for k, v := range e {
		values = append(values, k+"="+v)
	}
	return values
}

func testMap[K comparable, V any](values map[K]V) *csync.Map[K, V] {
	m := csync.NewMap[K, V]()
	m.Reset(values)
	return m
}
