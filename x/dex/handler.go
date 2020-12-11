package dex

import (
	"fmt"
	"strconv"

	"github.com/okex/okexchain/x/common"

	"github.com/okex/okexchain/x/common/perf"
	"github.com/okex/okexchain/x/dex/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/tendermint/tendermint/crypto/tmhash"
	"github.com/tendermint/tendermint/libs/log"
)

// NewHandler handles all "dex" type messages.
func NewHandler(k IKeeper) sdk.Handler {
	return func(ctx sdk.Context, msg sdk.Msg) (*sdk.Result, error) {
		ctx = ctx.WithEventManager(sdk.NewEventManager())
		logger := ctx.Logger().With("module", ModuleName)

		var handlerFun func() (*sdk.Result, error)
		var name string
		switch msg := msg.(type) {
		case MsgList:
			name = "handleMsgList"
			handlerFun = func() (*sdk.Result, error) {
				return handleMsgList(ctx, k, msg, logger)
			}
		case MsgDeposit:
			name = "handleMsgDeposit"
			handlerFun = func() (*sdk.Result, error) {
				return handleMsgDeposit(ctx, k, msg, logger)
			}
		case MsgWithdraw:
			name = "handleMsgWithDraw"
			handlerFun = func() (*sdk.Result, error) {
				return handleMsgWithDraw(ctx, k, msg, logger)
			}
		case MsgTransferOwnership:
			name = "handleMsgTransferOwnership"
			handlerFun = func() (*sdk.Result, error) {
				return handleMsgTransferOwnership(ctx, k, msg, logger)
			}
		case MsgConfirmOwnership:
			name = "handleMsgConfirmOwnership"
			handlerFun = func() (*sdk.Result, error) {
				return handleMsgConfirmOwnership(ctx, k, msg, logger)
			}
		case MsgCreateOperator:
			name = "handleMsgCreateOperator"
			handlerFun = func() (*sdk.Result, error) {
				return handleMsgCreateOperator(ctx, k, msg, logger)
			}
		case MsgUpdateOperator:
			name = "handleMsgUpdateOperator"
			handlerFun = func() (*sdk.Result, error) {
				return handleMsgUpdateOperator(ctx, k, msg, logger)
			}
		default:
			errMsg := fmt.Sprintf("unrecognized dex message type: %T", msg)
			return sdk.ErrUnknownRequest(errMsg).Result()
		}

		seq := perf.GetPerf().OnDeliverTxEnter(ctx, ModuleName, name)
		defer perf.GetPerf().OnDeliverTxExit(ctx, ModuleName, name, seq)

		res, err := handlerFun()
		common.SanityCheckHandler(res, err)
		return res, err
	}
}

func handleMsgList(ctx sdk.Context, keeper IKeeper, msg MsgList, logger log.Logger) (*sdk.Result, error) {

	if !keeper.GetTokenKeeper().TokenExist(ctx, msg.ListAsset) ||
		!keeper.GetTokenKeeper().TokenExist(ctx, msg.QuoteAsset) {
		return nil, types.ErrTokenPairExisted(msg.ListAsset, msg.QuoteAsset)
	}

	if _, exists := keeper.GetOperator(ctx, msg.Owner); !exists {
		return nil, types.ErrUnknownOperator(msg.Owner)
	}

	tokenPair := &TokenPair{
		BaseAssetSymbol:  msg.ListAsset,
		QuoteAssetSymbol: msg.QuoteAsset,
		InitPrice:        msg.InitPrice,
		MaxPriceDigit:    int64(DefaultMaxPriceDigitSize),
		MaxQuantityDigit: int64(DefaultMaxQuantityDigitSize),
		MinQuantity:      sdk.MustNewDecFromStr("0.00000001"),
		Owner:            msg.Owner,
		Delisting:        false,
		Deposits:         DefaultTokenPairDeposit,
		BlockHeight:      ctx.BlockHeight(),
	}

	// check whether a specific token pair exists with the symbols of base asset and quote asset
	// Note: aaa_bbb and bbb_aaa are actually one token pair
	if keeper.GetTokenPair(ctx, fmt.Sprintf("%s_%s", tokenPair.BaseAssetSymbol, tokenPair.QuoteAssetSymbol)) != nil ||
		keeper.GetTokenPair(ctx, fmt.Sprintf("%s_%s", tokenPair.QuoteAssetSymbol, tokenPair.BaseAssetSymbol)) != nil {
		return nil, types.ErrTokenPairExisted(tokenPair.BaseAssetSymbol, tokenPair.QuoteAssetSymbol)
	}

	// deduction fee
	feeCoins := keeper.GetParams(ctx).ListFee.ToCoins()
	err := keeper.GetSupplyKeeper().SendCoinsFromAccountToModule(ctx, msg.Owner, keeper.GetFeeCollector(), feeCoins)
	if err != nil {
		return nil, types.ErrInsufficientFeeCoins(feeCoins.String())
	}

	err2 := keeper.SaveTokenPair(ctx, tokenPair)
	if err2 != nil {
		return nil, types.ErrTokenPairSaveFailed(err2.Error())
	}

	logger.Debug(fmt.Sprintf("successfully handleMsgList: "+
		"BlockHeight: %d, Msg: %+v", ctx.BlockHeight(), msg))

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			sdk.EventTypeMessage,
			sdk.NewAttribute("list-asset", tokenPair.BaseAssetSymbol),
			sdk.NewAttribute("quote-asset", tokenPair.QuoteAssetSymbol),
			sdk.NewAttribute("init-price", tokenPair.InitPrice.String()),
			sdk.NewAttribute("max-price-digit", strconv.FormatInt(tokenPair.MaxPriceDigit, 10)),
			sdk.NewAttribute("max-size-digit", strconv.FormatInt(tokenPair.MaxQuantityDigit, 10)),
			sdk.NewAttribute("min-trade-size", tokenPair.MinQuantity.String()),
			sdk.NewAttribute("delisting", fmt.Sprintf("%t", tokenPair.Delisting)),
			sdk.NewAttribute(sdk.AttributeKeyFee, feeCoins.String()),
		),
	)

	return &sdk.Result{Events: ctx.EventManager().Events()}, nil
}

func handleMsgDeposit(ctx sdk.Context, keeper IKeeper, msg MsgDeposit, logger log.Logger) (*sdk.Result, error) {
	confirmOwnership, exist := keeper.GetConfirmOwnership(ctx, msg.Product)
	if exist && !ctx.BlockTime().After(confirmOwnership.Expire) {
		return nil, types.ErrInternal(msg.Product)
	}
	if sdkErr := keeper.Deposit(ctx, msg.Product, msg.Depositor, msg.Amount); sdkErr != nil {
		return nil, sdkErr
	}

	logger.Debug(fmt.Sprintf("successfully handleMsgDeposit: "+
		"BlockHeight: %d, Msg: %+v", ctx.BlockHeight(), msg))

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			sdk.EventTypeMessage,
			sdk.NewAttribute(sdk.AttributeKeyModule, ModuleName),
		),
	)

	return &sdk.Result{Events: ctx.EventManager().Events()}, nil

}

func handleMsgWithDraw(ctx sdk.Context, keeper IKeeper, msg MsgWithdraw, logger log.Logger) (*sdk.Result, error) {
	if sdkErr := keeper.Withdraw(ctx, msg.Product, msg.Depositor, msg.Amount); sdkErr != nil {
		return nil, sdkErr
	}

	logger.Debug(fmt.Sprintf("successfully handleMsgWithDraw: "+
		"BlockHeight: %d, Msg: %+v", ctx.BlockHeight(), msg))

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			sdk.EventTypeMessage,
			sdk.NewAttribute(sdk.AttributeKeyModule, ModuleName),
		),
	)

	return &sdk.Result{Events: ctx.EventManager().Events()}, nil
}

func handleMsgTransferOwnership(ctx sdk.Context, keeper IKeeper, msg MsgTransferOwnership,
	logger log.Logger) (*sdk.Result, error) {
	// validate
	tokenPair := keeper.GetTokenPair(ctx, msg.Product)
	if tokenPair == nil {
		return nil, types.ErrTokenPairNotFound(fmt.Sprintf("non-exist product: %s", msg.Product))
	}
	if !tokenPair.Owner.Equals(msg.FromAddress) {
		return nil, types.ErrUnauthorized(msg.FromAddress.String())
	}
	if _, exist := keeper.GetOperator(ctx, msg.ToAddress); !exist {
		return nil, types.ErrUnknownOperator(msg.ToAddress)
	}
	confirmOwnership, exist := keeper.GetConfirmOwnership(ctx, msg.Product)
	if exist && !ctx.BlockTime().After(confirmOwnership.Expire) {
		return nil, types.ErrInternal(msg.Product)
	}

	// withdraw
	if tokenPair.Deposits.IsPositive() {
		if err := keeper.Withdraw(ctx, msg.Product, msg.FromAddress, tokenPair.Deposits); err != nil {
			return nil, types.ErrInternal(err.Error())
		}
	}

	// deduction fee
	feeCoins := keeper.GetParams(ctx).TransferOwnershipFee.ToCoins()
	err := keeper.GetSupplyKeeper().SendCoinsFromAccountToModule(ctx, msg.FromAddress, keeper.GetFeeCollector(), feeCoins)
	if err != nil {
		return nil, types.ErrInsufficientCoins(feeCoins.String())
	}

	// set ConfirmOwnership
	expireTime := ctx.BlockTime().Add(keeper.GetParams(ctx).OwnershipConfirmWindow)
	confirmOwnership = &types.ConfirmOwnership{
		Product:     msg.Product,
		FromAddress: msg.FromAddress,
		ToAddress:   msg.ToAddress,
		Expire:      expireTime,
	}
	keeper.SetConfirmOwnership(ctx, confirmOwnership)

	logger.Debug(fmt.Sprintf("successfully handleMsgTransferOwnership: "+
		"BlockHeight: %d, Msg: %+v", ctx.BlockHeight(), msg))

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			sdk.EventTypeMessage,
			sdk.NewAttribute(sdk.AttributeKeyModule, ModuleName),
			sdk.NewAttribute(sdk.AttributeKeyFee, feeCoins.String()),
		),
	)
	return &sdk.Result{Events: ctx.EventManager().Events()}, nil
}

func handleMsgConfirmOwnership(ctx sdk.Context, keeper IKeeper, msg MsgConfirmOwnership, logger log.Logger) (*sdk.Result, error) {
	confirmOwnership, exist := keeper.GetConfirmOwnership(ctx, msg.Product)
	if !exist {
		return nil, types.ErrUnknownRequest(fmt.Sprintf("no transfer-ownership of list (%s) to confirm", msg.Address.String()))
	}
	if ctx.BlockTime().After(confirmOwnership.Expire) {
		// delete ownership confirming information
		keeper.DeleteConfirmOwnership(ctx, confirmOwnership.Product)
		return nil, types.ErrInternal(confirmOwnership.Expire.String())
	}
	if !confirmOwnership.ToAddress.Equals(msg.Address) {
		return nil, types.ErrUnauthorized(confirmOwnership.ToAddress.String())
	}

	tokenPair := keeper.GetTokenPair(ctx, msg.Product)
	if tokenPair == nil {
		return nil, types.ErrTokenPairNotFound(fmt.Sprintf("non-exist product: %s", msg.Product))
	}
	// transfer ownership
	tokenPair.Owner = msg.Address
	keeper.UpdateTokenPair(ctx, msg.Product, tokenPair)
	keeper.UpdateUserTokenPair(ctx, msg.Product, confirmOwnership.FromAddress, msg.Address)
	// delete ownership confirming information
	keeper.DeleteConfirmOwnership(ctx, confirmOwnership.Product)

	logger.Debug(fmt.Sprintf("successfully handleMsgConfirmOwnership: "+
		"BlockHeight: %d, Msg: %+v", ctx.BlockHeight(), msg))

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			sdk.EventTypeMessage,
			sdk.NewAttribute(sdk.AttributeKeyModule, ModuleName),
		),
	)
	return &sdk.Result{Events: ctx.EventManager().Events()}, nil
}

func handleMsgCreateOperator(ctx sdk.Context, keeper IKeeper, msg MsgCreateOperator, logger log.Logger) (*sdk.Result, error) {

	logger.Debug(fmt.Sprintf("handleMsgCreateOperator msg: %+v", msg))

	if _, isExist := keeper.GetOperator(ctx, msg.Owner); isExist {
		return nil, types.ErrExistOperator(msg.Owner)
	}
	operator := types.DEXOperator{
		Address:            msg.Owner,
		HandlingFeeAddress: msg.HandlingFeeAddress,
		Website:            msg.Website,
		InitHeight:         ctx.BlockHeight(),
		TxHash:             fmt.Sprintf("%X", tmhash.Sum(ctx.TxBytes())),
	}
	keeper.SetOperator(ctx, operator)

	// deduction fee
	feeCoins := keeper.GetParams(ctx).RegisterOperatorFee.ToCoins()
	err := keeper.GetSupplyKeeper().SendCoinsFromAccountToModule(ctx, msg.Owner, keeper.GetFeeCollector(), feeCoins)
	if err != nil {
		return nil, types.ErrInsufficientCoins(feeCoins.String())
	}

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			sdk.EventTypeMessage,
			sdk.NewAttribute(sdk.AttributeKeyModule, ModuleName),
			sdk.NewAttribute(sdk.AttributeKeyFee, feeCoins.String()),
		),
	)

	return &sdk.Result{Events: ctx.EventManager().Events()}, nil
}

func handleMsgUpdateOperator(ctx sdk.Context, keeper IKeeper, msg MsgUpdateOperator, logger log.Logger) (*sdk.Result, error) {

	logger.Debug(fmt.Sprintf("handleMsgUpdateOperator msg: %+v", msg))

	operator, isExist := keeper.GetOperator(ctx, msg.Owner)
	if !isExist {
		return nil, types.ErrUnknownOperator(msg.Owner)
	}
	if !operator.Address.Equals(msg.Owner) {
		return nil, types.ErrUnauthorized(operator.Address.String())
	}

	operator.HandlingFeeAddress = msg.HandlingFeeAddress
	operator.Website = msg.Website

	keeper.SetOperator(ctx, operator)

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			sdk.EventTypeMessage,
			sdk.NewAttribute(sdk.AttributeKeyModule, ModuleName),
		),
	)

	return &sdk.Result{Events: ctx.EventManager().Events()}, nil
}
