package voice

import (
	"fmt"
	"sync"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// Каждому собеседнику выделена отдельная дорожка. При повторном использовании
// места RTP сохраняет последовательность, чтобы декодер не отбросил новый голос.
type audioOutput struct {
	mu                         sync.Mutex
	track                      *webrtc.TrackLocalStaticRTP
	source                     string
	sequence, sequenceOffset   uint16
	timestamp, timestampOffset uint32
}

func (o *audioOutput) write(source string, packet *rtp.Packet) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.source != source {
		o.source = source
		o.sequenceOffset = o.sequence + 1 - packet.SequenceNumber
		o.timestampOffset = o.timestamp + 960 - packet.Timestamp
	}
	sequence := packet.SequenceNumber + o.sequenceOffset
	timestamp := packet.Timestamp + o.timestampOffset
	if int16(sequence-o.sequence) > 0 {
		o.sequence, o.timestamp = sequence, timestamp
	}
	outgoing := *packet
	outgoing.PaddingSize = 0
	outgoing.Header = rtp.Header{Version: 2, Marker: packet.Marker, SequenceNumber: sequence, Timestamp: timestamp}
	_ = o.track.WriteRTP(&outgoing)
}

func prepareOutputs(peer *webrtc.PeerConnection) ([]*audioOutput, error) {
	outputs := make([]*audioOutput, 0, roomCapacity-1)
	for i := 0; i < roomCapacity-1; i++ {
		id := fmt.Sprintf("speaker-%d", i)
		track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, id, id)
		if err != nil {
			return nil, err
		}
		sender, err := peer.AddTrack(track)
		if err != nil {
			return nil, err
		}
		outputs = append(outputs, &audioOutput{track: track})
		go drainAudioFeedback(sender)
	}
	return outputs, nil
}

func drainAudioFeedback(sender *webrtc.RTPSender) {
	buffer := make([]byte, 1500)
	for {
		if _, _, err := sender.Read(buffer); err != nil {
			return
		}
	}
}
