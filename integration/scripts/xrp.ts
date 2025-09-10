
import {
  AccountSet, AccountSetAsfFlags,
  Client,
  SignerListSet,
} from "xrpl";


async function fundNewWallet(client: Client) {
  const { wallet, balance } = await client.fundWallet();

  console.log(
    `\nWallet funded. Address: ${wallet.address}, Balance: ${balance}, PrivKey: ${wallet.privateKey} - PubKey: ${wallet.publicKey}`
  );

  return { wallet, balance };
}

async function CreateMultisig(walletAddresses: string[], quorum: number) {

  const client = new Client("wss://s.altnet.rippletest.net:51233");
  await client.connect();

  const { wallet: mainWallet, balance: mainBalance } = await fundNewWallet(client);

  const signerEntries = walletAddresses.map((address) => ({
    SignerEntry: {
      Account: address,
      SignerWeight: 1,
    },
  }));

  const signerListSetTx: SignerListSet = {
    TransactionType: "SignerListSet",
    Account: mainWallet.address,
    SignerQuorum: quorum, // Minimum weight required to authorize a transaction
    SignerEntries: signerEntries,
  };

  // 3. Prepare and submit the SignerListSet transaction
  const prepared = await client.autofill(signerListSetTx);
  // console.log("Prepared SignerListSet:", prepared);

  const signed = mainWallet.sign(prepared);
  // console.log("Signed SignerListSet:", signed);

  const result = await client.submitAndWait(signed.tx_blob);

  // console.log("SignerList Set Result:", result);

  // 5. Prepare for future multi-signed transactions
  // console.log("Multi-Sig Account Setup Complete");
  // console.log("Main Account:", mainWallet.address);
  // console.log("Signers:", walletAddresses);

  const accountSetTx: AccountSet = {
    TransactionType: "AccountSet",
    Account: mainWallet.address,
    SetFlag: AccountSetAsfFlags.asfDisableMaster, // This disables the master key
  };

  const preparedAccountSet = await client.autofill(accountSetTx);
  const signedAccountSet = mainWallet.sign(preparedAccountSet);
  const accountSetResult = await client.submitAndWait(signedAccountSet.tx_blob);

  console.log("Master Key Disabled Result:", accountSetResult);

  console.log("Multi-Sig Account Setup Complete with Master Key Disabled");
  console.log("Main Account:", mainWallet.address);
  console.log("Signers:", walletAddresses);
  console.log("⚠️  WARNING: Master key is now disabled. Only multisig transactions are possible.");

  await client.disconnect();

  await client.disconnect();

  return [mainWallet.address, mainBalance]
}


export { CreateMultisig };