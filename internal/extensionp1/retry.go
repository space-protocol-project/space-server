package extensionp1

// RetryObservation задаётся доверенным транспортом, а не полями ответа расширения.
// SameRequest включает байты команды, ключ, сервер, instance и principal.
type RetryObservation struct {
	Code            string `json:"code"`
	Attempt         int    `json:"attempt"`
	ElapsedMS       int64  `json:"elapsed_ms"`
	RemainingMS     int64  `json:"remaining_ms"`
	WindowKnown     bool   `json:"window_known"`
	SameRequest     bool   `json:"same_request"`
	Cancelled       bool   `json:"cancelled"`
	PreviousUnknown bool   `json:"previous_unknown"`
}

type RetryDecision struct {
	Action  string `json:"action"`
	Unknown bool   `json:"unknown"`
	DelayMS int64  `json:"delay_ms"`
}

// DecideRetry — экспериментальная политика space.retry.p1, без отправки RPC.
// Unknown описывает неопределённость результата всей серии, не только последней попытки.
func DecideRetry(o RetryObservation) RetryDecision {
	if o.Attempt < 1 || o.Attempt > 4 || o.ElapsedMS < 0 || o.RemainingMS < 0 || !o.SameRequest {
		return RetryDecision{Action: "stop", Unknown: true}
	}
	if o.Code == "OK" {
		return RetryDecision{Action: "confirmed"}
	}
	ambiguous, known := retryCode(o.Code)
	d := RetryDecision{Action: "stop", Unknown: o.PreviousUnknown || ambiguous || !known}
	if o.Cancelled || !known || o.Code == "Canceled" {
		return d
	}
	if o.Code == "Unauthenticated" {
		d.Action = "reauthenticate"
		return d
	}
	if !ambiguous || o.Attempt == 4 || !o.WindowKnown {
		return d
	}
	delay := int64(250 << (o.Attempt - 1))
	// Сравнение через вычитание не допускает переполнения больших входных значений.
	if o.ElapsedMS > 30000-delay-5000 || o.RemainingMS < delay+5000 {
		return d
	}
	d.Action, d.DelayMS = "retry_same_request", delay
	return d
}

func retryCode(code string) (ambiguous, known bool) {
	switch code {
	case "Unknown", "Internal", "Unavailable", "DeadlineExceeded", "TransportLost", "Canceled":
		return true, true
	case "InvalidArgument", "NotFound", "AlreadyExists", "PermissionDenied", "ResourceExhausted", "FailedPrecondition", "Aborted", "OutOfRange", "Unimplemented", "DataLoss", "Unauthenticated":
		return false, true
	default:
		return false, false
	}
}
