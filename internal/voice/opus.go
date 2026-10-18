package voice

import "github.com/pion/webrtc/v4"

// NewOpusAPI регистрирует только аудиокодек Opus. Проверка разрешённых
// секций входящего SDP относится к будущему слою сигналинга.
// Это основа прототипа; к внешнему API Space пока не подключена.
func NewOpusAPI(settings webrtc.SettingEngine) (*webrtc.API, error) {
	engine := &webrtc.MediaEngine{}
	codec := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		},
		PayloadType: 111,
	}
	if err := engine.RegisterCodec(codec, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}
	return webrtc.NewAPI(webrtc.WithMediaEngine(engine), webrtc.WithSettingEngine(settings)), nil
}
