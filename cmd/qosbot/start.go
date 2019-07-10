package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/QOSGroup/cassini/log"
	qba "github.com/QOSGroup/qbase/account"
	"github.com/QOSGroup/qbase/client/account"
	qcliacc "github.com/QOSGroup/qbase/client/account"
	"github.com/QOSGroup/qbase/client/context"
	"github.com/QOSGroup/qbase/client/keys"
	qclitx "github.com/QOSGroup/qbase/client/tx"
	"github.com/QOSGroup/qbase/txs"
	"github.com/QOSGroup/qbase/types"
	"github.com/QOSGroup/qos/app"
	"github.com/QOSGroup/qos/cmd/qosbot/config"
	"github.com/QOSGroup/qos/module/stake"
	qostypes "github.com/QOSGroup/qos/types"
	"github.com/cihub/seelog"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	amino "github.com/tendermint/go-amino"
	"github.com/tendermint/tendermint/crypto"
	rpcclient "github.com/tendermint/tendermint/rpc/client"
	ctypes "github.com/tendermint/tendermint/rpc/core/types"
)

var secret string

// NewRootCommand returns a root command
func NewRootCommand() (cmd *cobra.Command) {
	cmd = &cobra.Command{
		Use:   "qosbot",
		Short: "a robot for blockchain qos",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) (err error) {
			if strings.EqualFold(cmd.Use, "version") ||
				strings.HasPrefix(cmd.Use, "help") {
				// doesn't need init log and config
				return nil
			}
			var logger seelog.LoggerInterface
			logger, err = log.LoadLogger(config.GetConfig().LogConfigFile)
			if err != nil {
				log.Warn("Used the default logger because error: ", err)
			} else {
				log.Replace(logger)
			}
			err = initConfig()
			if err != nil {
				return err
			}
			return
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			err := starter()
			if err != nil {
				fmt.Println("error: ", err)
				os.Exit(-1)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&config.GetConfig().ConfigFile, "config", "./qosbot.yml", "config file path")
	cmd.Flags().StringVar(&config.GetConfig().LogConfigFile, "log", "./log.conf", "log config file path")
	return
}

func initConfig() error {
	// init config
	conf := config.GetConfig()
	err := conf.Load()
	if err != nil {
		log.Error("Init config error: ", err.Error())
		return err
	}
	log.Info("Init config: ", conf.ConfigFile)

	log.Info("ticker: ", conf.Ticker)
	log.Info("node: ", conf.Node)
	log.Info("validator: ", conf.ValidatorAddress)
	log.Info("delegator: ", conf.DelegatorAddress)
	log.Info("wallet: ", conf.WalletAddress)
	log.Info("amount: ", conf.Amount)
	log.Info("max-gas: ", conf.MaxGas)
	log.Info("home: ", conf.Home)
	log.Info("chain-id: ", conf.ChainID)

	viper.Set("node", conf.Node)
	viper.Set("owner", conf.ValidatorAddress)
	viper.Set("delegator", conf.DelegatorAddress)
	viper.Set("tokens", conf.Amount)
	viper.Set("compound", false)
	viper.Set("home", conf.Home)
	viper.Set("chain-id", conf.ChainID)
	viper.Set("max-gas", conf.MaxGas)

	return nil
}

func starter() (err error) {
	log.Info("start...")

	defer func() {
		if r := recover(); r != nil {
			log := fmt.Sprintf("error: %v", r)
			err = errors.New(log)
		}
	}()

	conf := config.GetConfig()

	cdc := app.MakeCodec()
	secret, err = keys.ReadPassphraseFromStdin(conf.DelegatorAddress)

	err = doWork(cdc)
	if err != nil {
		log.Errorf("start error: %v", err)
		return err
	}

	tick := time.NewTicker(time.Millisecond * time.Duration(conf.Ticker))
	for range tick.C {
		err = doWork(cdc)
		if err != nil {
			log.Errorf("work error: %v", err)
		}
	}

	return nil
}

func doWork(cdc *amino.Codec) error {
	log.Debug("do work...")
	acc, err := queryAccount(cdc)
	if err != nil {
		log.Errorf("query account error: ", err)
	}
	log.Debugf("account: %s, qos: %s", acc.GetAddress(), acc.GetQOS())
	err = delegate(cdc)
	if err != nil {
		log.Errorf("delegate error: ", err)
	}
	log.Debug("work done.")
	return err
}

func queryAccount(cdc *amino.Codec) (*qostypes.QOSAccount, error) {
	cliCtx := context.NewCLIContext().WithCodec(cdc)
	var addr types.Address
	addr, err := account.GetAddrFromValue(cliCtx, config.GetConfig().DelegatorAddress)
	if err != nil {
		return nil, err
	}

	accInfo, err := queryAcct(cliCtx, addr)
	if err != nil {
		return nil, err
	}

	return accInfo, nil
}

func delegate(cdc *amino.Codec) error {
	txBuilder := func(ctx context.CLIContext) (txs.ITx, error) {

		tokens := viper.GetInt64("tokens")
		if tokens <= 0 {
			return nil, errors.New("delegate QOS amount must gt 0")
		}

		owner, err := qcliacc.GetAddrFromFlag(ctx, "owner")
		if err != nil {
			return nil, fmt.Errorf("cannot get owner's account: %v", err)
		}

		delegator, err := qcliacc.GetAddrFromFlag(ctx, "delegator")
		if err != nil {
			return nil, fmt.Errorf("cannot get delegator's account: %v", err)
		}

		return &stake.TxCreateDelegation{
			Delegator:      delegator,
			ValidatorOwner: owner,
			Amount:         uint64(tokens),
			IsCompound:     viper.GetBool("compound"),
		}, nil
	}

	// result, err := qclitx.BroadcastTx(cdc, txBuilder)
	result, err := broadcastTx(cdc, txBuilder)
	if err != nil {
		log.Errorf("broadcast tx error: %v", err)
		return err
	}
	log.Debugf("delegate result: %s", result)
	return nil
}

func broadcastTx(
	cdc *amino.Codec, txBuilder qclitx.ITxBuilder) (
	*ctypes.ResultBroadcastTxCommit, error) {
	cliCtx := context.NewCLIContext().WithCodec(cdc)
	signedTx, err := buildAndSignTx(cliCtx, txBuilder)
	if err != nil {
		return nil, err
	}

	return cliCtx.BroadcastTx(cdc.MustMarshalBinaryBare(signedTx))
}

func buildAndSignTx(
	ctx context.CLIContext, txBuilder qclitx.ITxBuilder) (
	signedTx types.Tx, err error) {
	// return nil, errors.New("not implements")
	defer func() {
		if r := recover(); r != nil {
			log := fmt.Sprintf("buildAndSignTx recovered: %v\n", r)
			signedTx = nil
			err = errors.New(log)
		}
	}()

	itx, err := txBuilder(ctx)
	if err != nil {
		return nil, err
	}
	toChainID := viper.GetString("chain-id")
	// qcpMode := viper.GetBool(cflags.FlagQcp)
	// if qcpMode {
	// 	fromChainID := viper.GetString(cflags.FlagQcpFrom)
	// 	return qclitx.BuildAndSignQcpTx(ctx, itx, fromChainID, toChainID)
	// }
	return buildAndSignStdTx(ctx, []txs.ITx{itx}, "", toChainID)
}

func buildAndSignStdTx(ctx context.CLIContext, tXs []txs.ITx,
	fromChainID, toChainID string) (*txs.TxStd, error) {

	accountNonce := viper.GetInt64("nonce")
	maxGas := viper.GetInt64("max-gas")
	if maxGas < 0 {
		return nil, errors.New("max-gas flag not correct")
	}

	txStd := txs.NewTxsStd(toChainID, types.NewInt(maxGas), tXs...)

	signers := getSigners(ctx, txStd.GetSigners())

	isUseFlagAccountNonce := accountNonce > 0
	for _, signerName := range signers {
		info, err := keys.GetKeyInfo(ctx, signerName)
		if err != nil {
			return nil, err
		}

		var actualNonce int64
		if isUseFlagAccountNonce {
			actualNonce = accountNonce + 1
		} else {
			nonce, err := getDefaultAccountNonce(ctx, info.GetAddress().Bytes())
			if err != nil || nonce < 0 {
				return nil, err
			}
			actualNonce = nonce + 1
		}

		txStd, err = signStdTx(ctx, signerName, actualNonce, txStd, fromChainID)
		if err != nil {
			return nil, fmt.Errorf("name %s signStdTx error: %s", signerName, err.Error())
		}
	}

	return txStd, nil
}

func getSigners(ctx context.CLIContext, txSignerAddrs []types.Address) []string {

	var sortNames []string

	for _, addr := range txSignerAddrs {

		keybase, err := keys.GetKeyBase(ctx)
		if err != nil {
			panic(err.Error())
		}

		info, err := keybase.GetByAddress(addr)
		if err != nil {
			panic(fmt.Sprintf("signer addr: %s not in current keybase. err:%s", addr, err.Error()))
		}

		sortNames = append(sortNames, info.GetName())
	}

	return sortNames
}

func getDefaultAccountNonce(ctx context.CLIContext, address []byte) (int64, error) {

	if ctx.NonceNodeURI == "" {
		return account.GetAccountNonce(ctx, address)
	}

	//NonceNodeURI不为空,使用NonceNodeURI查询account nonce值
	rpc := rpcclient.NewHTTP(ctx.NonceNodeURI, "/websocket")
	newCtx := context.NewCLIContext().WithClient(rpc).WithCodec(ctx.Codec)

	return account.GetAccountNonce(newCtx, address)
}

func signStdTx(ctx context.CLIContext, signerKeyName string, nonce int64, txStd *txs.TxStd, fromChainID string) (*txs.TxStd, error) {

	info, err := keys.GetKeyInfo(ctx, signerKeyName)
	if err != nil {
		return nil, err
	}

	addr := info.GetAddress()
	ok := false

	for _, signer := range txStd.GetSigners() {
		if bytes.Equal(addr.Bytes(), signer.Bytes()) {
			ok = true
		}
	}

	if !ok {
		return nil, fmt.Errorf("Name %s is not signer", signerKeyName)
	}

	sigdata := txStd.BuildSignatureBytes(nonce, fromChainID)
	sig, pubkey := signData(ctx, signerKeyName, sigdata)

	txStd.Signature = append(txStd.Signature, txs.Signature{
		Pubkey:    pubkey,
		Signature: sig,
		Nonce:     nonce,
	})

	return txStd, nil
}

func signData(ctx context.CLIContext, name string, data []byte) (
	[]byte, crypto.PubKey) {

	// pass, err := keys.GetPassphrase(ctx, name)
	// if err != nil {
	// 	panic(fmt.Sprintf("Get %s Passphrase error: %s", name, err.Error()))
	// }
	pass := secret

	keybase, err := keys.GetKeyBase(ctx)
	if err != nil {
		panic(err.Error())
	}

	sig, pubkey, err := keybase.Sign(name, pass, data)
	if err != nil {
		panic(err.Error())
	}

	return sig, pubkey
}

func queryAcct(ctx context.CLIContext, addr []byte) (*qostypes.QOSAccount, error) {
	path := qba.BuildAccountStoreQueryPath()
	res, err := ctx.Query(string(path), qba.AddressStoreKey(addr))
	if err != nil {
		return nil, err
	}

	if len(res) == 0 {
		return nil, account.ErrAccountNotExsits
	}

	var acc qostypes.QOSAccount
	err = ctx.Codec.UnmarshalBinaryBare(res, &acc)
	if err != nil {
		return nil, err
	}
	return &acc, nil
}
