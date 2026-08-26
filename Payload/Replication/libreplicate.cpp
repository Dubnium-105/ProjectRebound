// libreplicate.cpp : Defines the functions for the static library.
//

#include "../framework.h"
#include "../Replication/libreplicate.h"
#include "../Replication/ReplicationWorldGatePolicy.h"
#include <iostream>

#include "../SDK.hpp"

LibReplicate::LibReplicate(EReplicationMode ReplicationMode, void* InitListenFuncPtr, void* CreateChannelFuncPtr, void* SetChannelActorFuncPtr, void* ReplicateActorFuncPtr, void* FMemoryMallocFuncPtr, void* FMemoryFreeFuncPtr, void* OrigNotifyControlMessageFuncPtr, void* CreateNamedNetDriverFuncPtr, void* ActorChannelCloseFuncPtr, void* SetWorldFuncPtr, void* CallPreReplicationFuncPtr, void* SendClientAdjustmentFuncPtr) {
	this->JoinMode = EJoinMode::Closed;
	this->ReplicationMode = ReplicationMode;

	this->CreateChannelFuncPtr = (CreateChannel)CreateChannelFuncPtr;
	this->SetChannelActorFuncPtr = (SetChannelActor)SetChannelActorFuncPtr;
	this->ReplicateActorFuncPtr = (ReplicateActor)ReplicateActorFuncPtr;
	this->FMemoryMallocFuncPtr = (FMemoryMalloc)FMemoryMallocFuncPtr;
	this->FMemoryFreeFuncPtr = (FMemoryFree)FMemoryFreeFuncPtr;
	this->OrigNotifyControlMessageFuncPtr = (OrigNotifyControlMessage)OrigNotifyControlMessageFuncPtr;
	this->CreateNamedNetDriverFuncPtr = (CreateNamedNetDriver)CreateNamedNetDriverFuncPtr;
	this->SetWorldFuncPtr = (SetWorld)SetWorldFuncPtr;
	this->ActorChannelCloseFuncPtr = (UActorChannelClose)ActorChannelCloseFuncPtr;
	this->InitListenFuncPtr = (InitListen)InitListenFuncPtr;
	this->CallPreReplicationFuncPtr = (CallPreReplication)CallPreReplicationFuncPtr;
	this->SendClientAdjustmentFuncPtr = (SendClientAdjustment)SendClientAdjustmentFuncPtr;

	this->Channels = new std::vector<std::pair<UNetConnection*, std::vector<std::pair<UActorChannel*, AActor*>>>>();
	this->SentTemporaries = new std::vector<std::pair<UNetConnection*, std::vector<AActor*>>>();
	this->ChannelsToClose = new std::vector<UNetConnection*>();
}

void LibReplicate::CreateNetDriver(void* Engine, void* World, void* NetDriverName) {
	UEngine* CastEngine = (UEngine*)Engine;
	UWorld* CastWorld = (UWorld*)World;

	void** fuck = (void**)NetDriverName;

	CreateNamedNetDriverFuncPtr(CastEngine, CastWorld, *fuck, *fuck);
}

bool LibReplicate::CanJoinGame() {
	return true;
}

bool LibReplicate::JustReturnOne() {
	return true;
}

void LibReplicate::DoNothing() {
	return;
}

void LibReplicate::Listen(void* NetDriver, void* World, EJoinMode InitialJoinMode, int Port) {
	this->JoinMode = InitialJoinMode;

	FURL* URL = (FURL*)(FMemoryMallocFuncPtr(sizeof(FURL), 8));

	*URL = FURL();

	URL->Port = Port;

	FString* Error = (FString*)(FMemoryMallocFuncPtr(sizeof(FString), 8));

	*Error = FString();

	this->InitListenFuncPtr(NetDriver, World, URL, false, Error);

	this->SetWorldFuncPtr(NetDriver, World);
}

void LibReplicate::SetJoinMode(EJoinMode NewJoinMode) {
	this->JoinMode = NewJoinMode;
}

void LibReplicate::ResetForWorldChange() {
	std::scoped_lock lock(this->ChannelsToCloseMutex);
	// The NetDriver and its connection survive our owned seamless travel, but
	// individual actor channels do not have to. This vector is not an owner and
	// cannot keep channel pointers authoritative across the boundary. Clear it;
	// GetChannelForActor scans the connection's native OpenChannels first and
	// therefore still reuses a genuinely retained PlayerController channel.
	this->Channels->clear();
	this->SentTemporaries->clear();
	// Native NotifyActorDestroyed owns the actual channel close. This legacy
	// queue has never been dispatched; discard its non-owning pointers before
	// the source channel objects can be reclaimed.
	this->ChannelsToClose->clear();
}

bool LibReplicate::IsOpenActorChannelForActor(
	UNetConnection* Connection,
	UActorChannel* ActorChannel,
	AActor* Actor) const {
	if (!Connection || !ActorChannel || !Actor)
		return false;

	auto* NativeConnection =
		reinterpret_cast<SDK::UNetConnection*>(Connection);
	auto* NativeChannel =
		reinterpret_cast<SDK::UActorChannel*>(ActorChannel);
	auto* NativeActor = reinterpret_cast<SDK::AActor*>(Actor);
	bool isStillOpen = false;
	for (SDK::UChannel* OpenChannel : NativeConnection->OpenChannels) {
		if (OpenChannel == NativeChannel) {
			isStillOpen = true;
			break;
		}
	}

	// Do not dereference a cached channel until the native connection has
	// proven it still owns that exact pointer.
	if (!isStillOpen)
		return false;

	const bool actorStillMatches =
		NativeChannel->IsA(SDK::UActorChannel::StaticClass()) &&
		NativeChannel->Actor == NativeActor;
	return ReplicationWorldGatePolicy::IsCachedActorChannelUsable(
		isStillOpen, actorStillMatches);
}

LibReplicate::UActorChannel* LibReplicate::GetChannelForActor(UNetConnection* Connection, AActor* Actor) {
	for (auto& Pair : *(this->Channels)) {
		if (Pair.first == Connection) {
			for (auto it = Pair.second.begin(); it != Pair.second.end();) {
				if (it->second != Actor) {
					++it;
					continue;
				}

				if (IsOpenActorChannelForActor(
						Connection, it->first, Actor)) {
					return it->first;
				}

				// Seamless cleanup may clear Channel->Actor before destroying the
				// channel object. Evict that non-owning cache entry immediately.
				it = Pair.second.erase(it);
			}

			break;
		}
	}

	// The compatibility cache is not the channel authority.  Native seamless
	// travel can keep a channel alive across the world boundary, and older
	// payloads may already have discarded its cache entry.  Reuse the exact
	// open native actor channel before considering CreateChannel.
	auto* NativeConnection = reinterpret_cast<SDK::UNetConnection*>(Connection);
	auto* NativeActor = reinterpret_cast<SDK::AActor*>(Actor);
	if (NativeConnection && NativeActor) {
		for (SDK::UChannel* OpenChannel : NativeConnection->OpenChannels) {
			if (!OpenChannel ||
				!OpenChannel->IsA(SDK::UActorChannel::StaticClass())) {
				continue;
			}

			auto* ActorChannel = static_cast<SDK::UActorChannel*>(OpenChannel);
			if (ActorChannel->Actor != NativeActor)
				continue;

			auto* CachedChannel = reinterpret_cast<UActorChannel*>(ActorChannel);
			AddActorChannelToChannels(Connection, CachedChannel, Actor);
			return CachedChannel;
		}
	}

	return nullptr;
}

void LibReplicate::AddActorChannelToChannels(UNetConnection* Connection, UActorChannel* ActorChannel, AActor* Actor) {
	for (auto& Pair : *(this->Channels)) {
		if (Pair.first == Connection) {
			Pair.second.erase(std::remove_if(
				Pair.second.begin(), Pair.second.end(),
				[this, Connection, ActorChannel, Actor](const auto& Existing) {
					if (Existing.first != ActorChannel &&
						Existing.second != Actor) {
						return false;
					}
					return !IsOpenActorChannelForActor(
						Connection, Existing.first, Existing.second);
				}), Pair.second.end());
			for (const auto& Existing : Pair.second) {
				if (Existing.first == ActorChannel && Existing.second == Actor)
					return;
			}
			Pair.second.push_back(std::make_pair(ActorChannel, Actor));

			return;
		}
	}

	this->Channels->push_back(std::make_pair(Connection, std::vector<std::pair<UActorChannel*, AActor*>>()));

	this->AddActorChannelToChannels(Connection, ActorChannel, Actor);

	return;
}

bool LibReplicate::HaveWeSentThisTemporaryActor(UNetConnection* Connection, AActor* Actor) {
	for (auto& Pair : *(this->SentTemporaries)) {
		if (Pair.first == Connection) {
			for (AActor* CmpActor : Pair.second) {
				if (CmpActor == Actor)
					return true;
			}

			break;
		}
	}

	return false;
}

void LibReplicate::CallFromTickFlushHook(std::vector<FActorInfo>& Actors, std::vector<FPlayerControllerInfo>& PlayerControllers, std::vector<UNetConnection*>& Connections, void* ActorChannelName, UNetDriver* NetDriver) {
	for (auto const& ActorInfo : Actors) {
		this->CallPreReplicationFuncPtr(ActorInfo.ActorPtr, NetDriver);
	}

	for (auto const& PlayerControllerInfo : PlayerControllers) {
		auto* NativeConnection = reinterpret_cast<SDK::UNetConnection*>(
			PlayerControllerInfo.OwningConnection);
		auto* NativeController = NativeConnection
			? NativeConnection->PlayerController
			: nullptr;
		if (NativeController &&
			!ReplicationWorldGatePolicy::HasClientLoadedCurrentWorld(
				NativeController->SeamlessTravelCount,
				NativeController->LastCompletedSeamlessTravelCount)) {
			continue;
		}

		this->CallPreReplicationFuncPtr(PlayerControllerInfo.PlayerController, NetDriver);

		this->SendClientAdjustmentFuncPtr(PlayerControllerInfo.PlayerController);

		UActorChannel* Channel = GetChannelForActor(PlayerControllerInfo.OwningConnection, PlayerControllerInfo.PlayerController);

		if (!Channel) {

			Channel = this->CreateChannelFuncPtr(PlayerControllerInfo.OwningConnection, (FName*)ActorChannelName, 1 << 1, -1);

			if (Channel) {
				this->SetChannelActorFuncPtr(Channel, PlayerControllerInfo.PlayerController, 0);
				AddActorChannelToChannels(PlayerControllerInfo.OwningConnection, Channel, PlayerControllerInfo.PlayerController);
			}
		}

		if (Channel && IsOpenActorChannelForActor(
				PlayerControllerInfo.OwningConnection,
				Channel,
				PlayerControllerInfo.PlayerController)) {
			this->ReplicateActorFuncPtr(Channel);
		}
	}

	for (UNetConnection* Connection : Connections) {
		auto* NativeConnection = reinterpret_cast<SDK::UNetConnection*>(Connection);
		auto* NativeController = NativeConnection
			? NativeConnection->PlayerController
			: nullptr;
		if (NativeController &&
			!ReplicationWorldGatePolicy::HasClientLoadedCurrentWorld(
				NativeController->SeamlessTravelCount,
				NativeController->LastCompletedSeamlessTravelCount)) {
			continue;
		}

		for (auto const &ActorInfo : Actors) {
			if (ActorInfo.bNetTemporary && HaveWeSentThisTemporaryActor(Connection, ActorInfo.ActorPtr))
				continue;

			UActorChannel* Channel = GetChannelForActor(Connection, ActorInfo.ActorPtr);

			if (!Channel) {

				Channel = this->CreateChannelFuncPtr(Connection, (FName*)ActorChannelName, 1 << 1, -1);

				if (Channel) {
					this->SetChannelActorFuncPtr(Channel, ActorInfo.ActorPtr, 0);
					AddActorChannelToChannels(Connection, Channel, ActorInfo.ActorPtr);
				}
			}

			if (Channel && IsOpenActorChannelForActor(
					Connection, Channel, ActorInfo.ActorPtr)) {
				if (!(*(unsigned int*)((__int64)Channel + 0x88) & 0x2)) {
					*(unsigned int*)((__int64)Channel + 0x88) |= 2;
				}

				if (this->ReplicateActorFuncPtr(Channel)) {
					//std::cout << ((SDK::UObject*)ActorInfo.ActorPtr)->GetFullName() << std::endl;
				};
			}
		}
	}

	/*
	{
		std::scoped_lock t(this->ChannelsToCloseMutex);
		if (!this->ChannelsToClose->empty()) {
			while (this->ChannelsToClose->size() > 0) {
				UActorChannel* Channel = this->ChannelsToClose->back();

				this->ActorChannelCloseFuncPtr(Channel, 0);

				this->ChannelsToClose->pop_back();
			}
		}
	}*/
}

void LibReplicate::CallWhenActorDestroyed(FActorInfo& ActorInfo) {
	for (auto& pair : *(this->Channels)) {
		UActorChannel* Channel = GetChannelForActor(pair.first, ActorInfo.ActorPtr);

		if (Channel) {
			{
				std::scoped_lock t(this->ChannelsToCloseMutex);

				this->ChannelsToClose->push_back(Channel);
			}
		}

		pair.second.erase(std::remove_if(pair.second.begin(), pair.second.end(), [ActorInfo](const std::pair<UActorChannel*, AActor*> &test) {
			return test.second == ActorInfo.ActorPtr;
		}), pair.second.end());
	}

	if (ActorInfo.bNetTemporary) {
		for (auto& pair : *(this->SentTemporaries)) {
			pair.second.erase(std::remove_if(pair.second.begin(), pair.second.end(), [ActorInfo](const AActor* test) {
				return test == ActorInfo.ActorPtr;
			}), pair.second.end());
		}
	}
}
