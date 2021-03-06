// Code generated by easyjson for marshaling/unmarshaling. DO NOT EDIT.

package response

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

func easyjson120d1ca2DecodeGithubComSoulgardenKickexBotResponse(in *jlexer.Lexer, out *Order) {
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
		case "price":
			out.Price = string(in.String())
		case "amount":
			out.Amount = string(in.String())
		case "total":
			out.Total = string(in.String())
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
func easyjson120d1ca2EncodeGithubComSoulgardenKickexBotResponse(out *jwriter.Writer, in Order) {
	out.RawByte('{')
	first := true
	_ = first
	{
		const prefix string = ",\"price\":"
		out.RawString(prefix[1:])
		out.String(string(in.Price))
	}
	{
		const prefix string = ",\"amount\":"
		out.RawString(prefix)
		out.String(string(in.Amount))
	}
	{
		const prefix string = ",\"total\":"
		out.RawString(prefix)
		out.String(string(in.Total))
	}
	out.RawByte('}')
}

// MarshalJSON supports json.Marshaler interface
func (v Order) MarshalJSON() ([]byte, error) {
	w := jwriter.Writer{}
	easyjson120d1ca2EncodeGithubComSoulgardenKickexBotResponse(&w, v)
	return w.Buffer.BuildBytes(), w.Error
}

// MarshalEasyJSON supports easyjson.Marshaler interface
func (v Order) MarshalEasyJSON(w *jwriter.Writer) {
	easyjson120d1ca2EncodeGithubComSoulgardenKickexBotResponse(w, v)
}

// UnmarshalJSON supports json.Unmarshaler interface
func (v *Order) UnmarshalJSON(data []byte) error {
	r := jlexer.Lexer{Data: data}
	easyjson120d1ca2DecodeGithubComSoulgardenKickexBotResponse(&r, v)
	return r.Error()
}

// UnmarshalEasyJSON supports easyjson.Unmarshaler interface
func (v *Order) UnmarshalEasyJSON(l *jlexer.Lexer) {
	easyjson120d1ca2DecodeGithubComSoulgardenKickexBotResponse(l, v)
}
func easyjson120d1ca2DecodeGithubComSoulgardenKickexBotResponse1(in *jlexer.Lexer, out *GetOrder) {
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
		case "order":
			if in.IsNull() {
				in.Skip()
				out.Order = nil
			} else {
				if out.Order == nil {
					out.Order = new(AccountingOrder)
				}
				(*out.Order).UnmarshalEasyJSON(in)
			}
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
func easyjson120d1ca2EncodeGithubComSoulgardenKickexBotResponse1(out *jwriter.Writer, in GetOrder) {
	out.RawByte('{')
	first := true
	_ = first
	{
		const prefix string = ",\"id\":"
		out.RawString(prefix[1:])
		out.String(string(in.ID))
	}
	{
		const prefix string = ",\"order\":"
		out.RawString(prefix)
		if in.Order == nil {
			out.RawString("null")
		} else {
			(*in.Order).MarshalEasyJSON(out)
		}
	}
	out.RawByte('}')
}

// MarshalJSON supports json.Marshaler interface
func (v GetOrder) MarshalJSON() ([]byte, error) {
	w := jwriter.Writer{}
	easyjson120d1ca2EncodeGithubComSoulgardenKickexBotResponse1(&w, v)
	return w.Buffer.BuildBytes(), w.Error
}

// MarshalEasyJSON supports easyjson.Marshaler interface
func (v GetOrder) MarshalEasyJSON(w *jwriter.Writer) {
	easyjson120d1ca2EncodeGithubComSoulgardenKickexBotResponse1(w, v)
}

// UnmarshalJSON supports json.Unmarshaler interface
func (v *GetOrder) UnmarshalJSON(data []byte) error {
	r := jlexer.Lexer{Data: data}
	easyjson120d1ca2DecodeGithubComSoulgardenKickexBotResponse1(&r, v)
	return r.Error()
}

// UnmarshalEasyJSON supports easyjson.Unmarshaler interface
func (v *GetOrder) UnmarshalEasyJSON(l *jlexer.Lexer) {
	easyjson120d1ca2DecodeGithubComSoulgardenKickexBotResponse1(l, v)
}
