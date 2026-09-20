package config

import (
	"bytes"
	"errors"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

type expression struct {
	kind                        unstable.Kind
	key, table                  string
	start, valueStart, valueEnd int
	value                       string
}
type edit struct {
	start, end  int
	replacement []byte
}
type block struct {
	start, end int
	id         string
	data       []byte
}
type field struct {
	key   string
	value any
}

func expressions(raw []byte) ([]expression, error) {
	var parser unstable.Parser
	parser.Reset(raw)
	var out []expression
	table := ""
	for parser.NextExpression() {
		n := parser.Expression()
		if n.Kind != unstable.Table && n.Kind != unstable.ArrayTable && n.Kind != unstable.KeyValue {
			continue
		}
		var keys []string
		start := len(raw)
		it := n.Key()
		for it.Next() {
			k := it.Node()
			part := string(k.Data)
			if strings.Contains(part, ".") {
				part = "\x00" + part
			}
			keys = append(keys, part)
			if int(k.Raw.Offset) < start {
				start = int(k.Raw.Offset)
			}
		}
		for start > 0 && raw[start-1] != '\n' {
			start--
		}
		key := strings.Join(keys, ".")
		e := expression{kind: n.Kind, key: key, table: table, start: start}
		if n.Kind == unstable.Table || n.Kind == unstable.ArrayTable {
			table = key
			e.table = table
		} else {
			v := n.Value()
			rr := v.Raw
			if v.Kind == unstable.Bool {
				rr = parser.Range(v.Data)
			}
			e.valueStart, e.valueEnd, e.value = int(rr.Offset), int(rr.Offset+rr.Length), string(v.Data)
		}
		out = append(out, e)
	}
	return out, parser.Error()
}
func applyEdits(raw []byte, edits []edit) []byte {
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	out := append([]byte(nil), raw...)
	for _, e := range edits {
		next := make([]byte, 0, len(out)+len(e.replacement)-(e.end-e.start))
		next = append(next, out[:e.start]...)
		next = append(next, e.replacement...)
		next = append(next, out[e.end:]...)
		out = next
	}
	return out
}
func literal(v any) []byte {
	data, _ := toml.Marshal(map[string]any{"v": v})
	return bytes.TrimSpace(bytes.SplitN(data, []byte("="), 2)[1])
}
func patchFields(raw []byte, scope string, fields []field) ([]byte, error) {
	exprs, err := expressions(raw)
	if err != nil {
		return nil, err
	}
	wanted := map[string]any{}
	seen := map[string]bool{}
	for _, f := range fields {
		wanted[f.key] = f.value
	}
	var edits []edit
	insertAt := len(raw)
	for _, e := range exprs {
		if e.kind == unstable.Table || e.kind == unstable.ArrayTable {
			if e.key != scope && e.start < insertAt {
				insertAt = e.start
			}
			continue
		}
		if e.table != scope {
			continue
		}
		value, ok := wanted[e.key]
		if !ok {
			continue
		}
		if e.valueEnd <= e.valueStart || e.valueEnd > len(raw) {
			return nil, errors.New("cannot preserve this TOML field layout; edit configuration manually")
		}
		seen[e.key] = true
		encoded := literal(value)
		if text, ok := value.(string); ok && e.value == text {
			continue
		}
		if !bytes.Equal(raw[e.valueStart:e.valueEnd], encoded) {
			edits = append(edits, edit{e.valueStart, e.valueEnd, encoded})
		}
	}
	var add strings.Builder
	for _, f := range fields {
		if !seen[f.key] {
			if s, ok := f.value.(string); ok && s == "" {
				continue
			}
			if n, ok := f.value.(int); ok && n == 0 {
				continue
			}
			add.WriteString(f.key + " = " + string(literal(f.value)) + "\n")
		}
	}
	if add.Len() > 0 {
		text := add.String()
		if insertAt > 0 && raw[insertAt-1] != '\n' {
			text = "\n" + text
		}
		edits = append(edits, edit{insertAt, insertAt, []byte(text)})
	}
	return applyEdits(raw, edits), nil
}
func arrayBlocks(raw []byte, scope string) ([]block, error) {
	exprs, err := expressions(raw)
	if err != nil {
		return nil, err
	}
	var blocks []block
	active := -1
	for _, e := range exprs {
		header := e.kind == unstable.Table || e.kind == unstable.ArrayTable
		if header && active >= 0 && (e.key == scope || !strings.HasPrefix(e.key, scope+".")) {
			blocks[active].end = e.start
			active = -1
		}
		if e.kind == unstable.ArrayTable && e.key == scope {
			blocks = append(blocks, block{start: e.start, end: len(raw)})
			active = len(blocks) - 1
		}
		if active >= 0 && e.kind == unstable.KeyValue && e.table == scope && e.key == "id" {
			blocks[active].id = e.value
		}
	}
	for i := range blocks {
		blocks[i].data = raw[blocks[i].start:blocks[i].end]
	}
	return blocks, nil
}
func replaceBlocks(raw []byte, old []block, replacements [][]byte) []byte {
	var joined []byte
	for _, b := range replacements {
		if len(joined) > 0 && joined[len(joined)-1] != '\n' {
			joined = append(joined, '\n')
		}
		joined = append(joined, b...)
		if len(joined) > 0 && joined[len(joined)-1] != '\n' {
			joined = append(joined, '\n')
		}
	}
	if len(old) == 0 {
		out := append([]byte(nil), raw...)
		if len(out) > 0 && out[len(out)-1] != '\n' {
			out = append(out, '\n')
		}
		return append(out, joined...)
	}
	edits := []edit{{old[0].start, old[0].end, joined}}
	for _, b := range old[1:] {
		edits = append(edits, edit{b.start, b.end, nil})
	}
	return applyEdits(raw, edits)
}
func preserve(raw []byte, cfg Config) ([]byte, error) {
	exprs, err := expressions(raw)
	if err != nil {
		return nil, err
	}
	for _, e := range exprs {
		if e.kind == unstable.KeyValue && e.table == "" && (e.key == "connections" || e.key == "tui" || strings.HasPrefix(e.key, "tui.")) {
			return nil, errors.New("inline connection arrays or TUI settings cannot be preserved safely; use [[connections]] and [tui] tables")
		}
	}
	raw, err = patchFields(raw, "", []field{{"default_connection", cfg.DefaultConnection}})
	if err != nil {
		return nil, err
	}
	exprs, err = expressions(raw)
	if err != nil {
		return nil, err
	}
	start, end := -1, len(raw)
	for _, e := range exprs {
		if e.kind != unstable.Table && e.kind != unstable.ArrayTable {
			continue
		}
		if start >= 0 {
			end = e.start
			break
		}
		if e.kind == unstable.Table && e.key == "tui" {
			start = e.start
		}
	}
	b := []byte("\n[tui]\n")
	if start < 0 {
		start, end = len(raw), len(raw)
	} else {
		b = raw[start:end]
	}
	b, err = patchFields(b, "tui", []field{{"refresh_seconds", cfg.TUI.RefreshSeconds}, {"background_seconds", cfg.TUI.BackgroundSeconds}})
	if err != nil {
		return nil, err
	}
	raw = applyEdits(raw, []edit{{start, end, b}})
	old, err := arrayBlocks(raw, "connections")
	if err != nil {
		return nil, err
	}
	byID := map[string][]byte{}
	for _, b := range old {
		byID[b.id] = b.data
	}
	var out [][]byte
	for _, c := range cfg.Connections {
		b := byID[c.ID]
		if b == nil {
			b = []byte("\n[[connections]]\n")
		}
		b, err = patchFields(b, "connections", []field{{"id", c.ID}, {"name", c.Name}, {"kind", c.Kind}, {"binary", c.Binary}, {"config_path", c.ConfigPath}, {"profile", c.Profile}, {"ssh_host", c.SSHHost}, {"ssh_binary", c.SSHBinary}, {"ssh_config", c.SSHConfig}, {"ssh_profile", c.SSHProfile}, {"host", c.Host}, {"port", c.Port}, {"cert_path", c.CertPath}, {"secret_path", c.SecretPath}, {"socket_path", c.SocketPath}})
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return replaceBlocks(raw, old, out), nil
}
