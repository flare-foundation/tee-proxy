#!/usr/bin/env node

import { CreateMultisig } from './xrp';

interface CliArgs {
    walletAddresses: string[];
    quorum: number;
}

function parseArgs(): CliArgs {
    const args = process.argv.slice(2);

    if (args.length < 2) {
        console.error('Usage: ts-node cli.ts <quorum> <address1> [address2] [address3] ...');
        console.error('Example: ts-node cli.ts 2 rAddress1 rAddress2 rAddress3');
        process.exit(1);
    }

    const quorum = parseInt(args[0]);
    if (isNaN(quorum) || quorum < 1) {
        console.error('Error: Quorum must be a positive integer');
        process.exit(1);
    }

    const walletAddresses = args.slice(1);

    if (walletAddresses.length === 0) {
        console.error('Error: At least one wallet address is required');
        process.exit(1);
    }

    if (quorum > walletAddresses.length) {
        console.error('Error: Quorum cannot be greater than the number of wallet addresses');
        process.exit(1);
    }

    return { walletAddresses, quorum };
}

async function main() {
    try {
        const { walletAddresses, quorum } = parseArgs();

        // Create the multisig wallet
        const [multisigAddress, balance] = await CreateMultisig(walletAddresses, quorum);

        // Output the result in JSON format for easy parsing in Go
        const result = {
            success: true,
            multisigAddress,
            balance,
            error: "",
        };

        console.log(JSON.stringify(result));
    } catch (error) {
        // Output error in JSON format
        const errorResult = {
            success: false,
            error: error instanceof Error ? error.message : String(error)
        };

        console.error(JSON.stringify(errorResult));
        process.exit(1);
    }
}

if (require.main === module) {
    main();
}