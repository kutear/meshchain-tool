package main

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/spf13/viper"
)

// Config 配置
type Config struct {
	Global   GlobalConfig `mapstructure:"global"`
	Accounts []Account    `mapstructure:"accounts"`
}

// GlobalConfig 全局配置
type GlobalConfig struct {
	RequestInterval int64  `mapstructure:"request_interval"`
	BaseUrl         string `mapstructure:"base_url"`
	ProxyUrl        string `mapstructure:"proxy_url"`
}

// Account 结构体系
type Account struct {
	AccessToken     string   `mapstructure:"access_token"`
	RefreshToken    string   `mapstructure:"refresh_token"`
	UniqueIds       []string `mapstructure:"unique_ids"`
	Email           string   `mapstructure:"email"`
	UpdateTimestamp string   `mapstructure:"update_timestamp"`
}

// LoadConfig 加载配置文件
func LoadConfig(filename string) *Config {
	viper.SetConfigFile(filename)

	if err := viper.ReadInConfig(); err != nil {
		panic(fmt.Sprintf("加载配置文件失败, %v", err))
	}

	var config Config
	if err := viper.Unmarshal(&config); err != nil {
		panic(fmt.Sprintf("解析配置文件失败, %v", err))
	}

	return &config
}

// UpdateConfig 更新配置文件
func UpdateConfig(accounts []Account) error {
	// 读取现有配置
	var config map[string]interface{}
	if err := viper.Unmarshal(&config); err != nil {
		return fmt.Errorf("解析配置失败: %v", err)
	}

	// 手动更新 accounts 部分
	var accountsData []map[string]interface{}
	for _, account := range accounts {
		accountMap := map[string]interface{}{
			"access_token":     account.AccessToken,
			"refresh_token":    account.RefreshToken,
			"unique_ids":       account.UniqueIds,
			"email":            account.Email,
			"update_timestamp": account.UpdateTimestamp,
		}
		accountsData = append(accountsData, accountMap)
	}

	// 替换 accounts 部分
	config["accounts"] = accountsData

	// 写回配置文件
	viper.Set("accounts", accountsData) // 只更新内存中的配置
	if err := viper.WriteConfig(); err != nil {
		return fmt.Errorf("写入配置文件失败: %v", err)
	}

	// 加入短暂的等待时间
	time.Sleep(100 * time.Millisecond)

	return nil
}

// LoadHttpClient 加载 HTTP CLIENT
func LoadHttpClient() *http.Client {

	transport := &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, // 跳过对服务器 TLS 证书的验证
		MaxIdleConns:        100,                                   // 最大空闲连接数
		IdleConnTimeout:     90 * time.Second,                      // 空闲连接的最大存活时间
		TLSHandshakeTimeout: 10 * time.Second,                      // TLS 握手的最大时间
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second, // 连接超时
			KeepAlive: 30 * time.Second, // 保活时长
		}).DialContext,
	}

	// 配置代理信息
	if config.Global.ProxyUrl != "" {
		proxyUrl, err := url.Parse(config.Global.ProxyUrl)
		if err != nil {
			panic(fmt.Errorf("验证代理地址[%s]失败 %v", config.Global.ProxyUrl, err))
		}
		transport.Proxy = http.ProxyURL(proxyUrl)
	}

	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}
}

// CreateNode 创建节点
func CreateNode(uniqueId, accessToken string) error {

	payload := map[string]string{
		"unique_id": uniqueId,
		"node_type": "browser",
		"name":      "Extension",
	}
	thisHeaders := headers
	thisHeaders["Authorization"] = fmt.Sprintf("Bearer %s", accessToken)
	_, err := ExecuteMethod("nodes/link", "POST", payload, thisHeaders)
	if err != nil {
		return err
	}

	return nil
}

// StartNodeReward 开启节点的奖励
func StartNodeReward(uniqueId, accessToken string) error {

	payload := map[string]string{"unique_id": uniqueId}
	thisHeaders := headers
	thisHeaders["Authorization"] = fmt.Sprintf("Bearer %s", accessToken)
	_, err := ExecuteMethod("rewards/start", "POST", payload, thisHeaders)
	if err != nil {
		return err
	}

	return nil
}

// ExecuteMethod 执行 HTTP 请求
func ExecuteMethod(path, method string, payload, headers map[string]string) (map[string]interface{}, error) {

	uri := fmt.Sprintf("%s/%s", config.Global.BaseUrl, path)

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("序列化解析 payload 失败: %v", err)
	}

	req, err := http.NewRequest(method, uri, bytes.NewBuffer(data))

	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %v", err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(data)))

	maxRetires := 3 // 最大重试次数
	for i := 1; i <= maxRetires; i++ {

		resp, err := httpClient.Do(req)
		if err != nil {
			if strings.Contains(err.Error(), "context deadline exceeded") {
				logChannel <- fmt.Sprintf("请求超时 (第 %d 次尝试). 正在重试...\n", i)
				time.Sleep(time.Second * 2) // 等待一段时间后重试
				continue
			}
			return nil, fmt.Errorf("请求失败: %v", err)
		}

		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("读取响应数据失败: %v", err)
		}

		var result map[string]interface{}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("解析响应数据: %v", err)
		}

		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("接口处理失败: %v", resp.StatusCode)
		}
		return result, nil
	}
	return nil, fmt.Errorf("请求多次失败: context deadline exceeded")
}

// ClaimReward 领取积分奖励
func ClaimReward(uniqueId, accessToken string) error {

	payload := map[string]string{"unique_id": uniqueId}
	thisHeaders := headers
	thisHeaders["Authorization"] = fmt.Sprintf("Bearer %s", accessToken)
	_, err := ExecuteMethod("rewards/claim", "POST", payload, thisHeaders)
	if err != nil {
		return err
	}

	return nil
}

// EstimateReward 预估积分奖励
func EstimateReward(uniqueId, accessToken string) (float64, error) {

	payload := map[string]string{"unique_id": uniqueId}
	localHeaders := CopyHeaders()
	localHeaders["Authorization"] = fmt.Sprintf("Bearer %s", accessToken)
	result, err := ExecuteMethod("rewards/estimate", "POST", payload, localHeaders)
	if err != nil {
		return 0, err
	}
	value, ok := result["value"].(float64)
	if !ok {
		return 0, fmt.Errorf("预估积分奖励请求,响应数据格式错误")
	}
	return value, nil
}

// RefreshToken 刷新 token
func RefreshToken(expiredToken string) (string, string, error) {

	payload := map[string]string{"refresh_token": expiredToken}

	result, err := ExecuteMethod("auth/refresh-token", "POST", payload, headers)
	if err != nil {
		return "", "", err
	}

	accessToken, ok1 := result["access_token"].(string)
	refreshToken, ok2 := result["refresh_token"].(string)

	if !ok1 || !ok2 {
		return "", "", fmt.Errorf("刷新 token 请求,响应数据格式错误")
	}

	return accessToken, refreshToken, nil
}

// GenerateHex 生成 node unique id
func GenerateHex() string {
	b := make([]byte, 16) // 16 字节 => 32 个十六进制字符
	_, err := rand.Read(b)
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// CopyHeaders 生成单独的 headers
func CopyHeaders() map[string]string {
	copied := make(map[string]string)
	for k, v := range headers {
		copied[k] = v
	}
	return copied
}

// CheckJwtTokenExpiration 检查 access_token | refresh_token 是否已经过期
func CheckJwtTokenExpiration(tokenString string) (bool, error) {

	// 解析 Token 不验证签名
	token, _, err := new(jwt.Parser).ParseUnverified(tokenString, jwt.MapClaims{})

	if err != nil {
		return false, fmt.Errorf("解析 jwt token 失败: [%v]", err)
	}

	// 从 Claims 中获取 `exp` 字段
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return false, fmt.Errorf("jwt token 转换 claims 类型失败")
	}

	exp, ok := claims["exp"].(float64)
	if !ok {
		return false, fmt.Errorf("jwt token 中没有 exp 字段或格式错误")
	}

	// 比较 `exp` 时间戳和当前时间
	expirationTime := time.Unix(int64(exp), 0)

	if time.Now().After(expirationTime) {
		return false, nil // 已过期
	}
	return true, nil // 未过期
}

// ProcessAccount 每个账户单独的处理逻辑
func ProcessAccount(accountIndex int, buffer *strings.Builder) {

	// 获取最新的 account 数据
	mu.RLock()
	account := &config.Accounts[accountIndex]
	mu.RUnlock()

	// access_token 过期则生成新 access_token | refresh_token
	if valid, err := CheckJwtTokenExpiration(account.AccessToken); err == nil && !valid {

		buffer.WriteString(fmt.Sprintf("检查到 [%s] 的 token 过期进行刷新", account.Email))

		newAccessToken, newRefreshToken, err := RefreshToken(account.RefreshToken)
		if err != nil {
			buffer.WriteString(fmt.Sprintf("[%s] 刷新 token 操作失败,错误提示为: [%v]", account.Email, err))
			return
		}

		// 更新 account 的 token 信息
		mu.Lock()
		account.AccessToken = newAccessToken
		account.RefreshToken = newRefreshToken
		account.UpdateTimestamp = time.Now().Format("2006-01-02 15:04:05")
		// 等待主线程统一更新
		hasUpdates = true
		mu.Unlock()
	}

	// 如果 UniqueIds 为空 则生成一个新的节点
	if len(account.UniqueIds) == 0 {
		uniqueId := GenerateHex()
		err := CreateNode(uniqueId, account.AccessToken)
		if err != nil {
			buffer.WriteString(fmt.Sprintf("[%s] 生成 node 失败,需要手动处理. err: [%v]", account.Email, err))
			return
		}

		err = StartNodeReward(uniqueId, account.AccessToken)
		if err != nil {
			buffer.WriteString(fmt.Sprintf("[%s] 启动 node [%s] 失败,需要手动处理. err: [%v]", account.Email, uniqueId, err))
			return
		}

		// 更新 account 数据
		mu.Lock()
		account.UniqueIds = []string{uniqueId}
		account.UpdateTimestamp = time.Now().Format("2006-01-02 15:04:05")
		// 等待主线程统一更新
		hasUpdates = true
		mu.Unlock()
	}

	// 遍历 UniqueIds 处理奖励相关逻辑
	for _, uniqueId := range account.UniqueIds {
		var valueFormat float64
		value, err := EstimateReward(uniqueId, account.AccessToken)
		if err != nil {
			// 如果是 401 错误 刷新 token
			if strings.Contains(err.Error(), "401") {
				newAccessToken, newRefreshToken, err := RefreshToken(account.RefreshToken)
				if err != nil {
					buffer.WriteString(fmt.Sprintf("[%s] 刷新 token 失败: %v", account.Email, err))
					continue
				}

				// 更新 account 的 token 信息
				mu.Lock()
				account.AccessToken = newAccessToken
				account.RefreshToken = newRefreshToken
				account.UpdateTimestamp = time.Now().Format("2006-01-02 15:04:05")
				// 等待主线程统一更新
				hasUpdates = true
				mu.Unlock()

				// 尝试重新预估奖励
				value, err = EstimateReward(uniqueId, newAccessToken)
				if err != nil {
					buffer.WriteString(fmt.Sprintf("[%s] 更新后查询预估奖励失败, [%v]", account.Email, err))
					continue
				}
			} else {
				buffer.WriteString(fmt.Sprintf("[%s] 查询预估奖励失败, [%v]", account.Email, err))
				continue
			}
		}

		valueFormat = value
		if valueFormat == 0 {
			buffer.WriteString(fmt.Sprintf("[%s] 没有可以 claim 的奖励. 错误信息: [%v]\n", account.Email, err))
		} else if valueFormat < 25.2 {
			buffer.WriteString(fmt.Sprintf("[%s] 只有 [%v] 的奖励. 暂不 claim \n", account.Email, valueFormat))
		} else {
			err := ClaimReward(uniqueId, account.AccessToken)
			if err != nil {
				buffer.WriteString(fmt.Sprintf("[%s] claim [%v] 奖励失败, 错误: [%v]\n", account.Email, valueFormat, err))
			} else {
				buffer.WriteString(fmt.Sprintf("[%s] 成功 claim [%v] 奖励\n", account.Email, valueFormat))
			}
		}
	}
}

// StartLogWorker 日志处理 goroutine
func StartLogWorker() {
	go func() {
		for logMsg := range logChannel {
			log.Println(logMsg) // 按顺序输出日志
		}
	}()
}

var logChannel = make(chan string, 1000) // 日志队列 容量为1000
var hasUpdates bool
var mu sync.RWMutex                // 全局读写锁
var config *Config                 // 全局配置文件
var httpClient *http.Client        // 全局 http client
var configFileName = "config.toml" // 配置文件名称
var headers = map[string]string{
	"Content-Type": "application/json",
	"User-Agent":   "PostmanRuntime/7.29.0",
	"Host":         "api.meshchain.ai",
} // 全局 http 请求头

func main() {

	config = LoadConfig(configFileName)
	httpClient = LoadHttpClient()

	// 启动日志处理器
	StartLogWorker()

	for {
		var wg sync.WaitGroup

		logChannel <- fmt.Sprintf("开始 [%d] 个账号的处理", len(config.Accounts))

		// 每轮处理前重置标志
		hasUpdates = false

		// 日志缓存 用于按照账号顺序进行日志打印
		logCache := make([]string, len(config.Accounts))

		for index := range config.Accounts {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var logBuffer strings.Builder

				ProcessAccount(index, &logBuffer)
				// 存储日志
				logCache[index] = logBuffer.String()
			}()
		}
		wg.Wait()

		// 按照顺序输出日志
		for _, logMsg := range logCache {
			logChannel <- logMsg
		}

		if hasUpdates {

			logChannel <- fmt.Sprintf("有账号更新了数据,开始进行配置文件更新")

			mu.Lock()
			if err := UpdateConfig(config.Accounts); err != nil {
				logChannel <- fmt.Sprintf("更新配置文件失败: %v", err)
			} else {
				logChannel <- fmt.Sprintf("更新配置文件成功")
			}
			mu.Unlock()
		} else {
			logChannel <- fmt.Sprintf("没有更新的账号，跳过配置文件更新")
		}

		logChannel <- fmt.Sprintf("[%v] 个账户处理完毕\n", len(config.Accounts))
		logChannel <- fmt.Sprint("----------------------------------")
		time.Sleep(time.Duration(config.Global.RequestInterval) * time.Second)
	}

	// 关闭日志通道（在程序退出时）
	close(logChannel)
}
