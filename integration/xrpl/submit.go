package xrpl

import (

	// "1inch-tee-node/pkg/xrpl/utils"

	"fmt"
	"strconv"

	intactions "github.com/flare-foundation/tee-proxy/integration/actions"

	xrplclient "github.com/xrpscan/xrpl-go"
)

// XRPLResponse represents the structure of an XRPL response
type XRPLResponse struct {
	EngineResult        string `json:"engine_result"`
	EngineResultCode    int    `json:"engine_result_code"`
	EngineResultMessage string `json:"engine_result_message"`
	TransactionHash     string `json:"hash"`
}

// ExtractTransactionResult extracts the transaction result from an XRPL response
func ExtractTransactionResult(response map[string]interface{}) (*XRPLResponse, error) {
	// Extract the result from the response
	result, ok := response["result"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid response format: missing result")
	}

	xrplResp := &XRPLResponse{}

	// Extract engine result
	if engineResult, ok := result["engine_result"].(string); ok {
		xrplResp.EngineResult = engineResult
	}

	// Extract engine result code
	if engineResultCode, ok := result["engine_result_code"].(float64); ok {
		xrplResp.EngineResultCode = int(engineResultCode)
	}

	// Extract engine result message
	if engineResultMessage, ok := result["engine_result_message"].(string); ok {
		xrplResp.EngineResultMessage = engineResultMessage
	}

	txJson, ok := result["tx_json"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid response format: missing tx_json")
	}

	// Check what type the hash field is
	if hashValue, exists := txJson["hash"]; exists {
		// Try string assertion
		if txHash, ok := hashValue.(string); ok {
			fmt.Printf("Successfully extracted hash as string: %s\n", txHash)
			xrplResp.TransactionHash = txHash
		} else {
			// Hash is not a string, attempting to convert to string
			txHashStr := fmt.Sprintf("%v", hashValue)
			if txHashStr != "" && txHashStr != "<nil>" {
				fmt.Printf("Converted hash to string: %s\n", txHashStr)
				xrplResp.TransactionHash = txHashStr
			}
		}

		// return nil, fmt.Errorf("hash field exists but cannot convert to string: %v (type: %T)", hashValue, hashValue)
	}

	return xrplResp, nil
}

// SubmitMultisignedTx submits a multisigned payment transaction to XRPL
func SubmitMultisignedTx(client *xrplclient.Client, tx map[string]any, multisigSigners []intactions.SignerData) (*XRPLResponse, error) {
	// Convert multisigSigners to the format expected by XRPL
	multisigners := make([]map[string]interface{}, len(multisigSigners))
	for i, signer := range multisigSigners {
		multisigners[i] = map[string]interface{}{
			"Signer": map[string]interface{}{
				"Account":       signer.Account,
				"SigningPubKey": signer.SigningPubKey,
				"TxnSignature":  signer.TxnSignature,
			},
		}
	}

	tx["Signers"] = multisigners

	// Create the submit_multisigned request
	submitRequest := xrplclient.BaseRequest{
		"command": "submit_multisigned",
		"id":      1,
		"tx_json": tx,
	}

	// prettyJSON, err := json.MarshalIndent(submitRequest, "", "  ")
	// if err != nil {
	// 	log.Fatalf("Failed to marshal JSON: %s", err)
	// }

	// fmt.Printf("Submitting multisigned transaction: %s\n", prettyJSON)

	// Submit the multisigned transaction
	response, err := client.Request(submitRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to submit multisigned transaction: %w", err)
	}

	// fmt.Printf("Multisigned transaction response: %+v\n", response)

	txResponse, err := ExtractTransactionResult(response)
	if err != nil {
		return nil, fmt.Errorf("failed to extract transaction result: %w", err)
	}

	fmt.Printf("✅ Transaction submitted successfully! txResponse: %+v\n", txResponse)

	return txResponse, nil
}

// GetSequenceInfo gets the current sequence number for an XRPL account
func GetSequenceInfo(client *xrplclient.Client, walletAddress string) (uint32, error) {
	// Create account_info request
	accountInfoRequest := xrplclient.BaseRequest{
		"command": "account_info",
		"account": walletAddress,
		"ledger":  "validated",
	}

	// Make the request
	response, err := client.Request(accountInfoRequest)
	if err != nil {
		return 0, fmt.Errorf("failed to get account info: %w", err)
	}

	// Parse the response
	result, ok := response["result"].(map[string]interface{})
	if !ok {
		return 0, fmt.Errorf("invalid response format")
	}

	accountData, ok := result["account_data"].(map[string]interface{})
	if !ok {
		return 0, fmt.Errorf("account_data not found in response")
	}

	sequence, ok := accountData["Sequence"].(float64)
	if !ok {
		return 0, fmt.Errorf("sequence not found in account_data")
	}

	return uint32(sequence), nil
}

// GetCurrentLedgerSequence gets the current validated ledger sequence
func GetCurrentLedgerSequence(client *xrplclient.Client) (uint32, error) {
	// Create ledger request for current validated ledger
	ledgerRequest := xrplclient.BaseRequest{
		"command": "ledger",
		"ledger":  "validated",
	}

	// Make the request
	response, err := client.Request(ledgerRequest)
	if err != nil {
		return 0, fmt.Errorf("failed to get ledger info: %w", err)
	}

	// Parse the response
	result, ok := response["result"].(map[string]interface{})
	if !ok {
		return 0, fmt.Errorf("invalid response format")
	}

	ledger, ok := result["ledger"].(map[string]interface{})
	if !ok {
		return 0, fmt.Errorf("ledger not found in response")
	}

	ledgerIndex, ok := ledger["ledger_index"].(string)
	if !ok {
		return 0, fmt.Errorf("ledger_index not found in ledger")
	}

	ledgerIndexInt, err := strconv.ParseInt(ledgerIndex, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("ledger_index is not a int64")
	}

	return uint32(ledgerIndexInt), nil
}

// GetTransactionParams gets both sequence and suggested last ledger sequence for a transaction
func GetTransactionParams(walletAddress string, ledgerOffset uint32) (sequence uint32, lastLedgerSequence uint32, err error) {
	// Create a new client
	client := xrplclient.NewClient(xrplclient.ClientConfig{
		URL: "wss://s.altnet.rippletest.net:51233",
	})
	defer client.Close() //nolint:errcheck

	// Get account sequence
	sequence, err = GetSequenceInfo(client, walletAddress)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get sequence: %w", err)
	}

	// Get current ledger sequence
	currentLedger, err := GetCurrentLedgerSequence(client)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get current ledger: %w", err)
	}

	// Calculate last ledger sequence (current + offset for transaction validity window)
	if ledgerOffset == 0 {
		ledgerOffset = 10 // Default 10 ledger window (~30-60 seconds)
	}
	lastLedgerSequence = currentLedger + ledgerOffset

	return sequence, lastLedgerSequence, nil
}
