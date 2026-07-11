package feishu

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// feishuSign implements Feishu custom-bot signed webhook algorithm:
//
//	string_to_sign = timestamp + "\n" + secret
//	sign = base64(hmac_sha256(key=string_to_sign, message=""))
func feishuSign(timestamp, secret string) string {
	stringToSign := fmt.Sprintf("%s\n%s", timestamp, secret)
	mac := hmac.New(sha256.New, []byte(stringToSign))
	// empty message body per Feishu docs
	mac.Write(nil)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
