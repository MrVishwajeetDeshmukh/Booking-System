package models

type ShowRequest struct {
	Name       string   `json:"name"`
	Seats      []string `json:"seats"`
	PricePaise int64    `json:"price_paise"`
}

type ReserveRequest struct {
	Seats []string `json:"seats"`
}
