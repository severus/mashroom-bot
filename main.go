package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	dialogflow "cloud.google.com/go/dialogflow/apiv2"
	translate "cloud.google.com/go/translate/apiv3"
	vision "cloud.google.com/go/vision/apiv1"
	bot "github.com/meinside/telegram-bot-go"
	"golang.org/x/oauth2/google"
	dialogflowpb "google.golang.org/genproto/googleapis/cloud/dialogflow/v2"
	translatepb "google.golang.org/genproto/googleapis/cloud/translate/v3"
)

const (
	targetLanguage  = "ru-RU"
	messageAppendix = "\n\n⚠️ Результаты распознавания и определения основаны на сервисах Google Cloud Vision и Google Cloud Translation. За любые несоответствия фактического названия гриба и результатов распознавания ответственны указанные решения и их производители. Мы рекомендуем вам брать только те грибы, в которых вы уверены на 100 %."
)

var botToken string

func init() {
	botToken = os.Getenv("BOT_TOKEN")
}

func main() {
	log.Print("starting server...")
	http.HandleFunc("/", handler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
		log.Printf("defaulting to port %s", port)
	}

	// Start HTTP server
	log.Printf("listening on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}

func handler(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()

	webhook, err := parseWebhook(r)
	if err != nil {
		log.Println("error parsing webhook:", err)
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}

	if err := validateWebhook(webhook); err != nil {
		log.Println("error validating webhook:", err)
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}

	if webhook.Message.HasText() {
		log.Printf("got webhook with text")
		err := processText(ctx, webhook)
		if err != nil {
			log.Println("error processing text message:", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		return
	}

	log.Printf("got webhook with photo")
	err = processPhoto(ctx, webhook)
	if err != nil {
		log.Println("error processing message with photo:", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
}

func parseWebhook(r *http.Request) (bot.Update, error) {
	var webhook bot.Update
	err := json.NewDecoder(r.Body).Decode(&webhook)
	return webhook, err
}

func validateWebhook(webhook bot.Update) error {
	if !webhook.HasMessage() {
		return errors.New("webhook: no message")
	}

	if !webhook.Message.HasText() && !webhook.Message.HasPhoto() {
		return errors.New("webhook: no text or photo")
	}
	return nil
}

func processText(ctx context.Context, webhook bot.Update) error {
	creds, err := google.FindDefaultCredentials(ctx)
	if err != nil {
		return err
	}
	replies, err := DetectIntentText(
		creds.ProjectID,
		strconv.FormatInt(webhook.Message.Chat.ID, 10),
		*webhook.Message.Text,
		targetLanguage,
	)
	if err != nil {
		return err
	}
	client := bot.NewClient(botToken)
	for _, reply := range replies {
		sent := client.SendMessage(
			webhook.Message.Chat.ID,
			reply,
			bot.OptionsSendMessage{},
		)
		if !sent.Ok {
			return fmt.Errorf("send message: %s", *sent.Description)
		}
	}
	return nil
}

func processPhoto(ctx context.Context, webhook bot.Update) error {
	url, err := fileURL(webhook.Message.LargestPhoto().FileID)
	if err != nil {
		return fmt.Errorf("error getting file URL: %v", err)
	}
	labels, err := DetectLabels(url)
	if err != nil {
		return fmt.Errorf("error detecting labels: %v", err)
	}
	if !hasAny([]string{"fungus", "mushroom"}, labels) {
		client := bot.NewClient(botToken)
		sent := client.SendMessage(
			webhook.Message.Chat.ID,
			"Увы, но на этом изображении грибов я не вижу."+messageAppendix,
			bot.OptionsSendMessage{}.
				SetReplyToMessageID(webhook.Message.MessageID), // show original message
		)
		if !sent.Ok {
			return fmt.Errorf("send message: %s", *sent.Description)
		}
		return nil
	}
	labels = filter(labels, []string{"fungus", "mushroom"})
	text := strings.Join(labels, ", ")
	//text, err = translateText(ctx, text)
	//if err != nil {
	//	// log error, send message with untranslated text
	//	log.Println("error translating text:", err)
	//}
	client := bot.NewClient(botToken)
	sent := client.SendMessage(
		webhook.Message.Chat.ID,
		"На этом изображении я вижу: "+text+messageAppendix,
		bot.OptionsSendMessage{}.
			SetReplyToMessageID(webhook.Message.MessageID), // show original message
	)
	if !sent.Ok {
		return fmt.Errorf("send message: %s", *sent.Description)
	}
	return nil
}

func DetectIntentText(projectID, sessionID, text, languageCode string) ([]string, error) {
	ctx := context.Background()

	sessionClient, err := dialogflow.NewSessionsClient(ctx)
	if err != nil {
		return nil, err
	}
	defer sessionClient.Close()

	if projectID == "" || sessionID == "" {
		return nil, errors.New(fmt.Sprintf("Received empty project (%s) or session (%s)", projectID, sessionID))
	}

	sessionPath := fmt.Sprintf("projects/%s/agent/sessions/%s", projectID, sessionID)
	textInput := dialogflowpb.TextInput{Text: text, LanguageCode: languageCode}
	queryTextInput := dialogflowpb.QueryInput_Text{Text: &textInput}
	queryInput := dialogflowpb.QueryInput{Input: &queryTextInput}
	request := dialogflowpb.DetectIntentRequest{Session: sessionPath, QueryInput: &queryInput}

	response, err := sessionClient.DetectIntent(ctx, &request)
	if err != nil {
		return nil, err
	}

	queryResult := response.GetQueryResult()
	fulfillmentMessages := queryResult.GetFulfillmentMessages()
	var out []string
	for _, msg := range fulfillmentMessages {
		if msg.GetText() == nil {
			continue
		}
		out = append(out, msg.GetText().Text...)
	}
	return out, nil
}

func fileURL(fileID string) (string, error) {
	client := bot.NewClient(botToken)
	file := client.GetFile(fileID)
	if !file.Ok {
		return "", errors.New(*file.Description)
	}
	return fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", botToken, *file.Result.FilePath), nil
}

func DetectLabels(url string) ([]string, error) {
	ctx := context.Background()

	client, err := vision.NewImageAnnotatorClient(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	image, err := vision.NewImageFromReader(resp.Body)
	if err != nil {
		return nil, err
	}
	annotations, err := client.DetectLabels(ctx, image, nil, 10)
	if err != nil {
		return nil, err
	}

	labels := make([]string, len(annotations))
	for i, annotation := range annotations {
		labels[i] = annotation.Description
	}

	return labels, nil
}

func hasAny(what []string, where []string) bool {
	for _, s1 := range what {
		s1 = strings.ToLower(s1)
		for _, s2 := range where {
			s2 = strings.ToLower(s2)
			if s1 == s2 {
				return true
			}
		}
	}
	return false
}

func filter(a, b []string) []string {
	m := make(map[string]struct{})
	for _, s := range b {
		m[s] = struct{}{}
	}
	keep := func(s string) bool {
		_, ok := m[s]
		return !ok
	}
	c := make([]string, 0)
	for _, x := range a {
		if keep(x) {
			c = append(c, x)
		}
	}
	return c
}

func translateText(ctx context.Context, text string) (string, error) {
	creds, err := google.FindDefaultCredentials(ctx)
	if err != nil {
		return "", err
	}
	client, err := translate.NewTranslationClient(ctx)
	if err != nil {
		return "", err
	}
	defer client.Close()

	const sourceLanguage = "en-US"
	req := &translatepb.TranslateTextRequest{
		Parent:             fmt.Sprintf("projects/%s/locations/global", creds.ProjectID),
		SourceLanguageCode: sourceLanguage,
		TargetLanguageCode: targetLanguage,
		MimeType:           "text/plain", // Mime types: "text/plain", "text/html"
		Contents:           []string{text},
	}
	resp, err := client.TranslateText(ctx, req)
	if err != nil {
		return "", err
	}
	translations := resp.GetTranslations()
	out := make([]string, len(translations))
	for i, translation := range translations {
		out[i] = translation.GetTranslatedText()
	}
	return strings.Join(out, ", "), nil
}
