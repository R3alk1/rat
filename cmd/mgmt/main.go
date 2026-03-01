package main

import (
	"io"
	"log"
	"net/http"
)

const HOST = "http://c2-server:8081"

// прокси пересылает входящий http-запрос на c2-сервер и возвращает его ответ
func proxy(w http.ResponseWriter, r *http.Request) {
	target := HOST + r.URL.Path // формирование целевого адреса
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	request, err := http.NewRequest(r.Method, target, r.Body) // создание исходящего запроса
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	request.Header = r.Header
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		http.Error(w, err.Error(), 502) // если c2 недоступен
		return
	}
	defer response.Body.Close()
	// все заголовки ответа копируются
	for key, values := range response.Header {
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}
	w.WriteHeader(response.StatusCode) // передается статус
	io.Copy(w, response.Body)          // тело ответа
}

func main() {
	fs := http.FileServer(http.Dir("./web"))
	http.Handle("/", fs)
	http.HandleFunc("/api/", proxy) // проброс апи и папки установок через прокси
	http.HandleFunc("/download/", proxy)
	log.Println("Management web-interface at http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
