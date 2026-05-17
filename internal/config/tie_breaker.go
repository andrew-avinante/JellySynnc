package config

type TieBreaker struct {
	Field string `mapstructure:"field"`
	Value string `mapstructure:"value"`
}

func (tb *TieBreaker) GetField() string {
	if tb == nil {
		return ""
	}
	return tb.Field
}

func (tb *TieBreaker) GetValue() string {
	if tb == nil {
		return ""
	}
	return tb.Value
}
