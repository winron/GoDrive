package controller

import (
	"GoDrive/db"
	"encoding/json"
	"fmt"
	"net/http"
)

type userResponse struct {
	StatusCode int    `json:"code"`
	Msg        string `json:"msg"`
}

func userErrorResp(s int, msg string) userResponse {
	return userResponse{StatusCode: s, Msg: msg}
}

func returnUserRespJSON(w http.ResponseWriter, v userResponse) {
	js, err := json.Marshal(v)
	if err != nil {
		e := fmt.Sprintf("Failed to create json obj %s\n", err.Error())
		panic(e)
	}

	if v.StatusCode != 200 {
		w.WriteHeader(v.StatusCode)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(js)
}

// RegisterHandler handles user registration. Method: POST
func RegisterHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var regInfo db.RegInfo
		if r.Body == nil {
			resp := userErrorResp(http.StatusInternalServerError, "request body is empty")
			returnUserRespJSON(w, resp)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&regInfo)

	}
}
