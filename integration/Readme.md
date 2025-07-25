# TEE Proxy Integration Tests

Integration tests for the TEE (Trusted Execution Environment) proxy system that validate wallet management, transaction signing, and policy initialization.

## Main Test Flow

`TestInitializeSigningPolicy` orchestrates the complete workflow:
1. **Setup** - Initialize Redis, servers, and TEE identity
2. **Policy** - Create signing policy with 100 voters and 3 admin keys  
3. **Wallet** - Generate wallet within TEE
4. **Transaction** - Sign payment using generated wallet
5. **Backup** - Retrieve wallet backup for recovery

## Core Functions

### Test Functions
- `initializePolicy()` - Creates signing policy with random voters and enqueues initialization
- `getTeeInfo()` - Retrieves TEE identity and public key via challenge-response
- `generateWallet()` - Creates wallet with admin keys and validates existence proof
- `signTransaction()` - Signs payment transaction using TEE wallet
- `getBackup()` - Retrieves wallet backup data for recovery scenarios
- `deleteWallet()` - Deletes wallet and cleans up storage (commented out)

### Infrastructure
- `newProxyConfig()` - Sets up Redis, queues, storage, and voting components
- `runProxyServers()` - Starts external/internal servers and instruction forwarding

### Utilities
- `GenerateRandomKeys()` - Creates ECDSA key pairs for voters
- `BuildInstructionData()` - Constructs TEE instruction with ABI encoding
- `SignAndSendInstructions()` - Signs and sends instructions to proxy
- `GetActionResponse()` - Polls for action completion and validates results
- `VerifyReceipts()` - Validates signed instruction receipts

