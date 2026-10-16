package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/space-protocol-project/space-server/internal/home"
)

func runDoctor(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(output)
	data := flags.String("home-data", "", "Каталог данных пространства; только чтение")
	fingerprint := flags.String("fingerprint", "", "SHA-256 сертификата из карточки пространства")
	serverID := flags.String("server-id", "", "Server ID из карточки пространства")
	wan := flags.String("wan-ip", "", "WAN IP со страницы состояния роутера (необязательно)")
	asJSON := flags.Bool("json", false, "Машиночитаемый отчёт")
	var endpoints []home.Endpoint
	flags.Func("endpoint", "HTTPS адрес для проверки с этого компьютера; до восьми адресов", func(value string) error {
		if len(endpoints) >= 8 {
			return fmt.Errorf("Не более восьми адресов")
		}
		endpoint, err := home.EndpointFromOrigin(value)
		if err != nil {
			return err
		}
		endpoints = append(endpoints, endpoint)
		return nil
	})
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || (*data == "") == (len(endpoints) == 0) {
		return fmt.Errorf("Укажите home-data или endpoint с fingerprint и server-id")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	report, err := home.Diagnose(ctx, home.DoctorOptions{Data: *data, Endpoints: endpoints,
		Fingerprint: *fingerprint, ServerID: *serverID, WANIP: *wan})
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(output).Encode(report)
	}
	for _, check := range report.Checks {
		fmt.Fprintf(output, "[%s] %s %s\n", check.Status, check.Target, home.DoctorExplanation(check.Code))
	}
	if report.ExternalCheckedAt > 0 {
		fmt.Fprintf(output, "Последнее внешнее подключение: %s\n", time.UnixMilli(report.ExternalCheckedAt).UTC().Format(time.RFC3339))
	}
	return nil
}
