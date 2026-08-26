cmake_minimum_required(VERSION 3.20)
project(ProjectReboundCommandProtocolTests LANGUAGES CXX)

enable_testing()

function(enable_strict_warnings target)
    if(MSVC)
        target_compile_options(${target} PRIVATE /W4 /WX /utf-8)
    else()
        target_compile_options(${target} PRIVATE -Wall -Wextra -Wpedantic -Werror)
    endif()
endfunction()

add_executable(command_protocol_tests
    CommandProtocolTests.cpp
    ../Communication/CommandProtocol.cpp
)

target_compile_features(command_protocol_tests PRIVATE cxx_std_20)
enable_strict_warnings(command_protocol_tests)

add_test(NAME command_protocol_tests COMMAND command_protocol_tests)

add_executable(command_line_policy_tests
    CommandLinePolicyTests.cpp
)

target_compile_features(command_line_policy_tests PRIVATE cxx_std_20)
enable_strict_warnings(command_line_policy_tests)

add_test(NAME command_line_policy_tests COMMAND command_line_policy_tests)

add_executable(local_qos_discovery_policy_tests
    LocalQosDiscoveryPolicyTests.cpp
)

target_compile_features(local_qos_discovery_policy_tests PRIVATE cxx_std_20)
enable_strict_warnings(local_qos_discovery_policy_tests)

add_test(
    NAME local_qos_discovery_policy_tests
    COMMAND local_qos_discovery_policy_tests)

add_executable(unreal_container_tests
    UnrealContainerTests.cpp
)

target_compile_features(unreal_container_tests PRIVATE cxx_std_20)
enable_strict_warnings(unreal_container_tests)
if(MSVC)
    # Generated Unreal container declarations intentionally use anonymous
    # unions and SDK-width integer constants. Keep project code strict while
    # suppressing those two warnings for this focused regression harness.
    target_compile_options(unreal_container_tests PRIVATE /wd4201 /wd4245)
else()
    target_compile_options(unreal_container_tests PRIVATE -Wno-pedantic)
endif()

add_test(NAME unreal_container_tests COMMAND unreal_container_tests)

add_executable(loadout_state_policy_tests
    LoadoutStatePolicyTests.cpp
)

target_compile_features(loadout_state_policy_tests PRIVATE cxx_std_20)
enable_strict_warnings(loadout_state_policy_tests)

add_test(NAME loadout_state_policy_tests COMMAND loadout_state_policy_tests)

add_executable(respawn_state_policy_tests
    RespawnStatePolicyTests.cpp
)

target_compile_features(respawn_state_policy_tests PRIVATE cxx_std_20)
enable_strict_warnings(respawn_state_policy_tests)

add_test(NAME respawn_state_policy_tests COMMAND respawn_state_policy_tests)

add_executable(managed_possession_sync_policy_tests
    ManagedPossessionSyncPolicyTests.cpp
)

target_compile_features(managed_possession_sync_policy_tests PRIVATE cxx_std_20)
enable_strict_warnings(managed_possession_sync_policy_tests)

add_test(
    NAME managed_possession_sync_policy_tests
    COMMAND managed_possession_sync_policy_tests)

add_executable(seamless_intro_camera_policy_tests
    SeamlessIntroCameraPolicyTests.cpp
)

target_compile_features(seamless_intro_camera_policy_tests PRIVATE cxx_std_20)
enable_strict_warnings(seamless_intro_camera_policy_tests)

add_test(
    NAME seamless_intro_camera_policy_tests
    COMMAND seamless_intro_camera_policy_tests)

add_executable(dedicated_multi_match_policy_tests
    DedicatedMultiMatchPolicyTests.cpp
)

target_compile_features(dedicated_multi_match_policy_tests PRIVATE cxx_std_20)
enable_strict_warnings(dedicated_multi_match_policy_tests)

add_test(
    NAME dedicated_multi_match_policy_tests
    COMMAND dedicated_multi_match_policy_tests)

add_executable(join_ui_sync_policy_tests
    JoinUiSyncPolicyTests.cpp
)

target_compile_features(join_ui_sync_policy_tests PRIVATE cxx_std_20)
enable_strict_warnings(join_ui_sync_policy_tests)

add_test(NAME join_ui_sync_policy_tests COMMAND join_ui_sync_policy_tests)

add_executable(replication_world_gate_policy_tests
    ReplicationWorldGatePolicyTests.cpp
)

target_compile_features(replication_world_gate_policy_tests PRIVATE cxx_std_20)
enable_strict_warnings(replication_world_gate_policy_tests)

add_test(
    NAME replication_world_gate_policy_tests
    COMMAND replication_world_gate_policy_tests)

add_executable(direct_match_ui_cleanup_policy_tests
    DirectMatchUiCleanupPolicyTests.cpp
)

target_compile_features(direct_match_ui_cleanup_policy_tests PRIVATE cxx_std_20)
enable_strict_warnings(direct_match_ui_cleanup_policy_tests)

add_test(NAME direct_match_ui_cleanup_policy_tests COMMAND direct_match_ui_cleanup_policy_tests)

add_executable(archive_completion_policy_tests
    ArchiveCompletionPolicyTests.cpp
)

target_compile_features(archive_completion_policy_tests PRIVATE cxx_std_20)
enable_strict_warnings(archive_completion_policy_tests)

add_test(NAME archive_completion_policy_tests COMMAND archive_completion_policy_tests)

add_executable(weapon_archive_policy_tests
    WeaponArchivePolicyTests.cpp
)

target_compile_features(weapon_archive_policy_tests PRIVATE cxx_std_20)
enable_strict_warnings(weapon_archive_policy_tests)

add_test(NAME weapon_archive_policy_tests COMMAND weapon_archive_policy_tests)

if(WIN32)
    add_executable(command_framework_tests
        CommandFrameworkTests.cpp
        ../Communication/CommandFramework.cpp
        ../Communication/CommandProtocol.cpp
    )

    target_compile_features(command_framework_tests PRIVATE cxx_std_20)
    enable_strict_warnings(command_framework_tests)
    target_link_libraries(command_framework_tests PRIVATE Advapi32)

    add_test(NAME command_framework_tests COMMAND command_framework_tests)
endif()
