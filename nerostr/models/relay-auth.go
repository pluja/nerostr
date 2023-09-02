package models

type ApiOutputMessage struct {
	Id     string `json:"id"`
	Action string `json:"action"`
	Msg    string `json:"msg"`
}
