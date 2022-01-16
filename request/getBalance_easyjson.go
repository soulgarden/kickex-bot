// Code generated by easyjson for marshaling/unmarshaling. DO NOT EDIT.

package request

import (
	json "encoding/json"
	easyjson "github.com/mailru/easyjson"
	jlexer "github.com/mailru/easyjson/jlexer"
	jwriter "github.com/mailru/easyjson/jwriter"
)

// suppress unused package warning
var (
	_ *json.RawMessage
	_ *jlexer.Lexer
	_ *jwriter.Writer
	_ easyjson.Marshaler
)

func easyjson3eca66e8DecodeGithubComSoulgardenKickexBotRequest(in *jlexer.Lexer, out *GetBalance) {
	isTopLevel := in.IsStart()
	if in.IsNull() {
		if isTopLevel {
			in.Consumed()
		}
		in.Skip()
		return
	}
	in.Delim('{')
	for !in.IsDelim('}') {
		key := in.UnsafeFieldName(false)
		in.WantColon()
		if in.IsNull() {
			in.Skip()
			in.WantComma()
			continue
		}
		switch key {
		case "id":
			out.ID = string(in.String())
		case "type":
			out.Type = string(in.String())
		default:
			in.SkipRecursive()
		}
		in.WantComma()
	}
	in.Delim('}')
	if isTopLevel {
		in.Consumed()
	}
}
func easyjson3eca66e8EncodeGithubComSoulgardenKickexBotRequest(out *jwriter.Writer, in GetBalance) {
	out.RawByte('{')
	first := true
	_ = first
	{
		const prefix string = ",\"id\":"
		out.RawString(prefix[1:])
		out.String(string(in.ID))
	}
	{
		const prefix string = ",\"type\":"
		out.RawString(prefix)
		out.String(string(in.Type))
	}
	out.RawByte('}')
}

// MarshalJSON supports json.Marshaler interface
func (v GetBalance) MarshalJSON() ([]byte, error) {
	w := jwriter.Writer{}
	easyjson3eca66e8EncodeGithubComSoulgardenKickexBotRequest(&w, v)
	return w.Buffer.BuildBytes(), w.Error
}

// MarshalEasyJSON supports easyjson.Marshaler interface
func (v GetBalance) MarshalEasyJSON(w *jwriter.Writer) {
	easyjson3eca66e8EncodeGithubComSoulgardenKickexBotRequest(w, v)
}

// UnmarshalJSON supports json.Unmarshaler interface
func (v *GetBalance) UnmarshalJSON(data []byte) error {
	r := jlexer.Lexer{Data: data}
	easyjson3eca66e8DecodeGithubComSoulgardenKickexBotRequest(&r, v)
	return r.Error()
}

// UnmarshalEasyJSON supports easyjson.Unmarshaler interface
func (v *GetBalance) UnmarshalEasyJSON(l *jlexer.Lexer) {
	easyjson3eca66e8DecodeGithubComSoulgardenKickexBotRequest(l, v)
}
