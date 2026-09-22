package lsp

type testEnv map[string]string

func (e testEnv) Get(key string) string { return e[key] }
func (e testEnv) Env() []string {
	values := make([]string, 0, len(e))
	for k, v := range e {
		values = append(values, k+"="+v)
	}
	return values
}
