package logger

import (
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var defaultRedactExtraKeys = []string{
	"token",
	"email",
	"key",
	"api_key",
	"x_api_key",
	"authorization",
}

type redactCore struct {
	core      zapcore.Core
	extraKeys []string
	keySet    map[string]struct{}
}

func newRedactCore(core zapcore.Core, extraKeys []string) zapcore.Core {
	merged := mergeRedactKeys(extraKeys)
	return &redactCore{
		core:      core,
		extraKeys: merged,
		keySet:    buildRedactKeySet(merged),
	}
}

func (r *redactCore) Enabled(level zapcore.Level) bool {
	return r.core.Enabled(level)
}

func (r *redactCore) With(fields []zapcore.Field) zapcore.Core {
	return &redactCore{
		core:      r.core.With(redactFields(fields, r.keySet, r.extraKeys)),
		extraKeys: r.extraKeys,
		keySet:    r.keySet,
	}
}

func (r *redactCore) Check(entry zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if !r.Enabled(entry.Level) {
		return ce
	}
	return ce.AddCore(entry, r)
}

func (r *redactCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	entry.Message = logredact.RedactText(entry.Message, r.extraKeys...)
	return r.core.Write(entry, redactFields(fields, r.keySet, r.extraKeys))
}

func (r *redactCore) Sync() error {
	return r.core.Sync()
}

func mergeRedactKeys(extraKeys []string) []string {
	merged := make([]string, 0, len(defaultRedactExtraKeys)+len(extraKeys))
	seen := make(map[string]struct{}, len(defaultRedactExtraKeys)+len(extraKeys))
	for _, key := range defaultRedactExtraKeys {
		key = normalizeRedactKey(key)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, key)
	}
	for _, key := range extraKeys {
		key = normalizeRedactKey(key)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, key)
	}
	return merged
}

func buildRedactKeySet(keys []string) map[string]struct{} {
	out := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = normalizeRedactKey(key)
		if key == "" {
			continue
		}
		out[key] = struct{}{}
	}
	return out
}

func normalizeRedactKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

func redactFields(fields []zapcore.Field, keySet map[string]struct{}, extraKeys []string) []zapcore.Field {
	if len(fields) == 0 {
		return nil
	}
	out := make([]zapcore.Field, 0, len(fields))
	for _, field := range fields {
		out = append(out, redactField(field, keySet, extraKeys))
	}
	return out
}

func redactField(field zapcore.Field, keySet map[string]struct{}, extraKeys []string) zapcore.Field {
	if field.Type == zapcore.NamespaceType {
		return field
	}
	if _, ok := keySet[normalizeRedactKey(field.Key)]; ok {
		return zap.String(field.Key, "[REDACTED]")
	}

	switch field.Type {
	case zapcore.StringType:
		return zap.String(field.Key, logredact.RedactText(field.String, extraKeys...))
	case zapcore.ByteStringType:
		if bs, ok := field.Interface.([]byte); ok {
			return zap.String(field.Key, logredact.RedactText(string(bs), extraKeys...))
		}
	case zapcore.BinaryType:
		if bs, ok := field.Interface.([]byte); ok {
			return zap.String(field.Key, logredact.RedactText(string(bs), extraKeys...))
		}
	case zapcore.ErrorType:
		if err, ok := field.Interface.(error); ok && err != nil {
			return zap.String(field.Key, logredact.RedactText(err.Error(), extraKeys...))
		}
	case zapcore.ReflectType, zapcore.ObjectMarshalerType, zapcore.ArrayMarshalerType:
		return zap.Any(field.Key, redactAny(field.Interface, extraKeys))
	case zapcore.StringerType:
		if str, ok := field.Interface.(fmt.Stringer); ok && str != nil {
			return zap.String(field.Key, logredact.RedactText(str.String(), extraKeys...))
		}
	}
	return field
}

func redactAny(value any, extraKeys []string) any {
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		return logredact.RedactText(v, extraKeys...)
	case []byte:
		return logredact.RedactText(string(v), extraKeys...)
	case error:
		return logredact.RedactText(v.Error(), extraKeys...)
	case map[string]any:
		return logredact.RedactMap(v, extraKeys...)
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, redactAny(item, extraKeys))
		}
		return out
	default:
		return value
	}
}
