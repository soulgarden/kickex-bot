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

func easyjsonBdf27360DecodeGithubComSoulgardenKickexBotRequest(in *jlexer.Lexer, out *GetUsersOpenOrders) {
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
		case "pair":
			out.Pair = string(in.String())
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
func easyjsonBdf27360EncodeGithubComSoulgardenKickexBotRequest(out *jwriter.Writer, in GetUsersOpenOrders) {
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
	{
		const prefix string = ",\"pair\":"
		out.RawString(prefix)
		out.String(string(in.Pair))
	}
	out.RawByte('}')
}

// MarshalJSON supports json.Marshaler interface
func (v GetUsersOpenOrders) MarshalJSON() ([]byte, error) {
	w := jwriter.Writer{}
	easyjsonBdf27360EncodeGithubComSoulgardenKickexBotRequest(&w, v)
	return w.Buffer.BuildBytes(), w.Error
}

// MarshalEasyJSON supports easyjson.Marshaler interface
func (v GetUsersOpenOrders) MarshalEasyJSON(w *jwriter.Writer) {
	easyjsonBdf27360EncodeGithubComSoulgardenKickexBotRequest(w, v)
}

// UnmarshalJSON supports json.Unmarshaler interface
func (v *GetUsersOpenOrders) UnmarshalJSON(data []byte) error {
	r := jlexer.Lexer{Data: data}
	easyjsonBdf27360DecodeGithubComSoulgardenKickexBotRequest(&r, v)
	return r.Error()
}

// UnmarshalEasyJSON supports easyjson.Unmarshaler interface
func (v *GetUsersOpenOrders) UnmarshalEasyJSON(l *jlexer.Lexer) {
	easyjsonBdf27360DecodeGithubComSoulgardenKickexBotRequest(l, v)
}
