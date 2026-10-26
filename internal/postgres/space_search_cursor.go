package postgres

import (
    "encoding/base64"
    "encoding/json"
    "io"
    "strings"
    pb "github.com/space-protocol-project/space-server/gen/space/v1"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/status"
)

type spaceSearchCursor struct {
    Version int `json:"v"`
    Binding string `json:"b"`
    Created int64 `json:"t"`
    Kind int32 `json:"k"`
    Channel string `json:"c"`
    ID string `json:"i"`
}

func parseSpaceSearchCursor(token, binding string) (spaceSearchCursor, error) {
    if token == "" { return spaceSearchCursor{}, nil }
    invalid := status.Error(codes.InvalidArgument, "Некорректная страница поиска пространства")
    if len(token) > 1024 { return spaceSearchCursor{}, invalid }
    data, err := base64.RawURLEncoding.Strict().DecodeString(token)
    if err != nil { return spaceSearchCursor{}, invalid }
    var cursor spaceSearchCursor
    decoder := json.NewDecoder(strings.NewReader(string(data)))
    decoder.DisallowUnknownFields()
    if decoder.Decode(&cursor) != nil || decoder.Decode(new(any)) != io.EOF { return cursor, invalid }
    if cursor.Version != 1 || cursor.Binding != binding || cursor.Created < 0 || cursor.Kind < 1 || cursor.Kind > 5 ||
        !channelIDPattern.MatchString(cursor.Channel) || !validForumText(cursor.ID, 256) { return cursor, invalid }
    return cursor, nil
}

func spaceSearchToken(result *pb.SpaceSearchResult, binding string) string {
    data, _ := json.Marshal(spaceSearchCursor{1, binding, result.CreatedAtUnixMs, int32(result.Kind), result.ChannelId, result.Id})
    return base64.RawURLEncoding.EncodeToString(data)
}
