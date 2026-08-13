package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

var (
	errInvalidJSON         = errors.New("invalid JSON")
	errDuplicateJSONMember = errors.New("duplicate JSON object member")
)

func rejectDuplicateJSONMembers(data []byte) error {
	if !utf8.Valid(data) {
		return errInvalidJSON
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); errors.Is(err, io.EOF) {
		return nil
	}
	return errInvalidJSON
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return errInvalidJSON
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return errInvalidJSON
			}
			key, ok := keyToken.(string)
			if !ok {
				return errInvalidJSON
			}
			if _, duplicate := seen[key]; duplicate {
				return errDuplicateJSONMember
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		return consumeClosingDelimiter(decoder, '}')
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		return consumeClosingDelimiter(decoder, ']')
	default:
		return errInvalidJSON
	}
}

func consumeClosingDelimiter(decoder *json.Decoder, want json.Delim) error {
	token, err := decoder.Token()
	if err != nil || token != want {
		return errInvalidJSON
	}
	return nil
}
