
import {
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

  await client.disconnect();

  return [mainWallet.address, mainBalance]
}


export { CreateMultisig };