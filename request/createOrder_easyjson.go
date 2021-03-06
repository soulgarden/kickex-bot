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

func easyjsonEd1192aDecodeGithubComSoulgardenKickexBotRequest(in *jlexer.Lexer, out *CreateOrderFields) {
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
		case "pair":
			out.Pair = string(in.String())
		case "ordered_volume":
			out.OrderedVolume = string(in.String())
		case "limit_price":
			out.LimitPrice = string(in.String())
		case "trade_intent":
			out.TradeIntent = int(in.Int())
		case "modifier":
			out.Modifier = int(in.Int())
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
func easyjsonEd1192aEncodeGithubComSoulgardenKickexBotRequest(out *jwriter.Writer, in CreateOrderFields) {
	out.RawByte('{')
	first := true
	_ = first
	{
		const prefix string = ",\"pair\":"
		out.RawString(prefix[1:])
		out.String(string(in.Pair))
	}
	{
		const prefix string = ",\"ordered_volume\":"
		out.RawString(prefix)
		out.String(string(in.OrderedVolume))
	}
	if in.LimitPrice != "" {
		const prefix string = ",\"limit_price\":"
		out.RawString(prefix)
		out.String(string(in.LimitPrice))
	}
	{
		const prefix string = ",\"trade_intent\":"
		out.RawString(prefix)
		out.Int(int(in.TradeIntent))
	}
	if in.Modifier != 0 {
		const prefix string = ",\"modifier\":"
		out.RawString(prefix)
		out.Int(int(in.Modifier))
	}
	out.RawByte('}')
}

// MarshalJSON supports json.Marshaler interface
func (v CreateOrderFields) MarshalJSON() ([]byte, error) {
	w := jwriter.Writer{}
	easyjsonEd1192aEncodeGithubComSoulgardenKickexBotRequest(&w, v)
	return w.Buffer.BuildBytes(), w.Error
}

// MarshalEasyJSON supports easyjson.Marshaler interface
func (v CreateOrderFields) MarshalEasyJSON(w *jwriter.Writer) {
	easyjsonEd1192aEncodeGithubComSoulgardenKickexBotRequest(w, v)
}

// UnmarshalJSON supports json.Unmarshaler interface
func (v *CreateOrderFields) UnmarshalJSON(data []byte) error {
	r := jlexer.Lexer{Data: data}
	easyjsonEd1192aDecodeGithubComSoulgardenKickexBotRequest(&r, v)
	return r.Error()
}

// UnmarshalEasyJSON supports easyjson.Unmarshaler interface
func (v *CreateOrderFields) UnmarshalEasyJSON(l *jlexer.Lexer) {
	easyjsonEd1192aDecodeGithubComSoulgardenKickexBotRequest(l, v)
}
func easyjsonEd1192aDecodeGithubComSoulgardenKickexBotRequest1(in *jlexer.Lexer, out *CreateOrder) {
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
		case "fields":
			if in.IsNull() {
				in.Skip()
				out.Fields = nil
			} else {
				if out.Fields == nil {
					out.Fields = new(CreateOrderFields)
				}
				(*out.Fields).UnmarshalEasyJSON(in)
			}
		case "externalId":
			out.ExternalID = string(in.String())
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
func easyjsonEd1192aEncodeGithubComSoulgardenKickexBotRequest1(out *jwriter.Writer, in CreateOrder) {
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
		const prefix string = ",\"fields\":"
		out.RawString(prefix)
		if in.Fields == nil {
			out.RawString("null")
		} else {
			(*in.Fields).MarshalEasyJSON(out)
		}
	}
	if in.ExternalID != "" {
		const prefix string = ",\"externalId\":"
		out.RawString(prefix)
		out.String(string(in.ExternalID))
	}
	out.RawByte('}')
}

// MarshalJSON supports json.Marshaler interface
func (v CreateOrder) MarshalJSON() ([]byte, error) {
	w := jwriter.Writer{}
	easyjsonEd1192aEncodeGithubComSoulgardenKickexBotRequest1(&w, v)
	return w.Buffer.BuildBytes(), w.Error
}

// MarshalEasyJSON supports easyjson.Marshaler interface
func (v CreateOrder) MarshalEasyJSON(w *jwriter.Writer) {
	easyjsonEd1192aEncodeGithubComSoulgardenKickexBotRequest1(w, v)
}

// UnmarshalJSON supports json.Unmarshaler interface
func (v *CreateOrder) UnmarshalJSON(data []byte) error {
	r := jlexer.Lexer{Data: data}
	easyjsonEd1192aDecodeGithubComSoulgardenKickexBotRequest1(&r, v)
	return r.Error()
}

// UnmarshalEasyJSON supports easyjson.Unmarshaler interface
func (v *CreateOrder) UnmarshalEasyJSON(l *jlexer.Lexer) {
	easyjsonEd1192aDecodeGithubComSoulgardenKickexBotRequest1(l, v)
}
