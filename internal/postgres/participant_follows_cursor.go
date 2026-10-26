package postgres

import (
 "encoding/base64"
 "encoding/json"
 "io"
 "strings"
 "google.golang.org/grpc/codes"
 "google.golang.org/grpc/status"
)

type followCursor struct {
 Version int `json:"v"`
 Binding string `json:"b"`
 Revision int64 `json:"r"`
 Created int64 `json:"t"`
 Channel string `json:"c"`
 ID string `json:"i"`
}

func parseFollowCursor(token,binding string,revision int64,feed bool) (followCursor,error) {
 if token=="" {return followCursor{},nil}
 invalid:=status.Error(codes.InvalidArgument,"Некорректная страница подписок")
 if len(token)>1024{return followCursor{},invalid}
 data,err:=base64.RawURLEncoding.Strict().DecodeString(token);if err!=nil{return followCursor{},invalid}
 var cursor followCursor
 decoder:=json.NewDecoder(strings.NewReader(string(data)));decoder.DisallowUnknownFields()
 if decoder.Decode(&cursor)!=nil||decoder.Decode(new(any))!=io.EOF{return cursor,invalid}
 if cursor.Version!=1||cursor.Binding!=binding||cursor.Created<0||!validFollowID(cursor.ID)||cursor.Revision<0{return cursor,invalid}
 if feed && !channelIDPattern.MatchString(cursor.Channel){return cursor,invalid}
 if !feed && (cursor.Channel!=""||cursor.Created!=0){return cursor,invalid}
 if cursor.Revision!=revision{return cursor,status.Error(codes.Aborted,"Подписки изменились; обновите ленту")}
 return cursor,nil
}

func encodeFollowCursor(cursor followCursor) string {
 data,_:=json.Marshal(cursor);return base64.RawURLEncoding.EncodeToString(data)
}
