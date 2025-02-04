// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.4
// 	protoc        v5.29.3
// source: pb/control.proto

package pb

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	reflect "reflect"
	sync "sync"
	unsafe "unsafe"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type Control struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Types that are valid to be assigned to Message:
	//
	//	*Control_Disconnect_
	//	*Control_Motd
	Message       isControl_Message `protobuf_oneof:"message"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Control) Reset() {
	*x = Control{}
	mi := &file_pb_control_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Control) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Control) ProtoMessage() {}

func (x *Control) ProtoReflect() protoreflect.Message {
	mi := &file_pb_control_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Control.ProtoReflect.Descriptor instead.
func (*Control) Descriptor() ([]byte, []int) {
	return file_pb_control_proto_rawDescGZIP(), []int{0}
}

func (x *Control) GetMessage() isControl_Message {
	if x != nil {
		return x.Message
	}
	return nil
}

func (x *Control) GetDisconnect() *Control_Disconnect {
	if x != nil {
		if x, ok := x.Message.(*Control_Disconnect_); ok {
			return x.Disconnect
		}
	}
	return nil
}

func (x *Control) GetMotd() *Control_MOTD {
	if x != nil {
		if x, ok := x.Message.(*Control_Motd); ok {
			return x.Motd
		}
	}
	return nil
}

type isControl_Message interface {
	isControl_Message()
}

type Control_Disconnect_ struct {
	Disconnect *Control_Disconnect `protobuf:"bytes,1,opt,name=disconnect,proto3,oneof"`
}

type Control_Motd struct {
	Motd *Control_MOTD `protobuf:"bytes,2,opt,name=motd,proto3,oneof"`
}

func (*Control_Disconnect_) isControl_Message() {}

func (*Control_Motd) isControl_Message() {}

type Control_MOTD struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Message       string                 `protobuf:"bytes,1,opt,name=message,proto3" json:"message,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Control_MOTD) Reset() {
	*x = Control_MOTD{}
	mi := &file_pb_control_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Control_MOTD) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Control_MOTD) ProtoMessage() {}

func (x *Control_MOTD) ProtoReflect() protoreflect.Message {
	mi := &file_pb_control_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Control_MOTD.ProtoReflect.Descriptor instead.
func (*Control_MOTD) Descriptor() ([]byte, []int) {
	return file_pb_control_proto_rawDescGZIP(), []int{0, 0}
}

func (x *Control_MOTD) GetMessage() string {
	if x != nil {
		return x.Message
	}
	return ""
}

type Control_Disconnect struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Reason        string                 `protobuf:"bytes,1,opt,name=reason,proto3" json:"reason,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Control_Disconnect) Reset() {
	*x = Control_Disconnect{}
	mi := &file_pb_control_proto_msgTypes[2]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Control_Disconnect) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Control_Disconnect) ProtoMessage() {}

func (x *Control_Disconnect) ProtoReflect() protoreflect.Message {
	mi := &file_pb_control_proto_msgTypes[2]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Control_Disconnect.ProtoReflect.Descriptor instead.
func (*Control_Disconnect) Descriptor() ([]byte, []int) {
	return file_pb_control_proto_rawDescGZIP(), []int{0, 1}
}

func (x *Control_Disconnect) GetReason() string {
	if x != nil {
		return x.Reason
	}
	return ""
}

var File_pb_control_proto protoreflect.FileDescriptor

var file_pb_control_proto_rawDesc = string([]byte{
	0x0a, 0x10, 0x70, 0x62, 0x2f, 0x63, 0x6f, 0x6e, 0x74, 0x72, 0x6f, 0x6c, 0x2e, 0x70, 0x72, 0x6f,
	0x74, 0x6f, 0x12, 0x02, 0x70, 0x62, 0x22, 0xbe, 0x01, 0x0a, 0x07, 0x43, 0x6f, 0x6e, 0x74, 0x72,
	0x6f, 0x6c, 0x12, 0x38, 0x0a, 0x0a, 0x64, 0x69, 0x73, 0x63, 0x6f, 0x6e, 0x6e, 0x65, 0x63, 0x74,
	0x18, 0x01, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x16, 0x2e, 0x70, 0x62, 0x2e, 0x43, 0x6f, 0x6e, 0x74,
	0x72, 0x6f, 0x6c, 0x2e, 0x44, 0x69, 0x73, 0x63, 0x6f, 0x6e, 0x6e, 0x65, 0x63, 0x74, 0x48, 0x00,
	0x52, 0x0a, 0x64, 0x69, 0x73, 0x63, 0x6f, 0x6e, 0x6e, 0x65, 0x63, 0x74, 0x12, 0x26, 0x0a, 0x04,
	0x6d, 0x6f, 0x74, 0x64, 0x18, 0x02, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x10, 0x2e, 0x70, 0x62, 0x2e,
	0x43, 0x6f, 0x6e, 0x74, 0x72, 0x6f, 0x6c, 0x2e, 0x4d, 0x4f, 0x54, 0x44, 0x48, 0x00, 0x52, 0x04,
	0x6d, 0x6f, 0x74, 0x64, 0x1a, 0x20, 0x0a, 0x04, 0x4d, 0x4f, 0x54, 0x44, 0x12, 0x18, 0x0a, 0x07,
	0x6d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x07, 0x6d,
	0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x1a, 0x24, 0x0a, 0x0a, 0x44, 0x69, 0x73, 0x63, 0x6f, 0x6e,
	0x6e, 0x65, 0x63, 0x74, 0x12, 0x16, 0x0a, 0x06, 0x72, 0x65, 0x61, 0x73, 0x6f, 0x6e, 0x18, 0x01,
	0x20, 0x01, 0x28, 0x09, 0x52, 0x06, 0x72, 0x65, 0x61, 0x73, 0x6f, 0x6e, 0x42, 0x09, 0x0a, 0x07,
	0x6d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x42, 0x20, 0x5a, 0x1e, 0x67, 0x69, 0x74, 0x68, 0x75,
	0x62, 0x2e, 0x63, 0x6f, 0x6d, 0x2f, 0x74, 0x61, 0x72, 0x69, 0x6b, 0x30, 0x32, 0x2f, 0x70, 0x72,
	0x6f, 0x78, 0x79, 0x68, 0x75, 0x62, 0x2f, 0x70, 0x62, 0x62, 0x06, 0x70, 0x72, 0x6f, 0x74, 0x6f,
	0x33,
})

var (
	file_pb_control_proto_rawDescOnce sync.Once
	file_pb_control_proto_rawDescData []byte
)

func file_pb_control_proto_rawDescGZIP() []byte {
	file_pb_control_proto_rawDescOnce.Do(func() {
		file_pb_control_proto_rawDescData = protoimpl.X.CompressGZIP(unsafe.Slice(unsafe.StringData(file_pb_control_proto_rawDesc), len(file_pb_control_proto_rawDesc)))
	})
	return file_pb_control_proto_rawDescData
}

var file_pb_control_proto_msgTypes = make([]protoimpl.MessageInfo, 3)
var file_pb_control_proto_goTypes = []any{
	(*Control)(nil),            // 0: pb.Control
	(*Control_MOTD)(nil),       // 1: pb.Control.MOTD
	(*Control_Disconnect)(nil), // 2: pb.Control.Disconnect
}
var file_pb_control_proto_depIdxs = []int32{
	2, // 0: pb.Control.disconnect:type_name -> pb.Control.Disconnect
	1, // 1: pb.Control.motd:type_name -> pb.Control.MOTD
	2, // [2:2] is the sub-list for method output_type
	2, // [2:2] is the sub-list for method input_type
	2, // [2:2] is the sub-list for extension type_name
	2, // [2:2] is the sub-list for extension extendee
	0, // [0:2] is the sub-list for field type_name
}

func init() { file_pb_control_proto_init() }
func file_pb_control_proto_init() {
	if File_pb_control_proto != nil {
		return
	}
	file_pb_control_proto_msgTypes[0].OneofWrappers = []any{
		(*Control_Disconnect_)(nil),
		(*Control_Motd)(nil),
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: unsafe.Slice(unsafe.StringData(file_pb_control_proto_rawDesc), len(file_pb_control_proto_rawDesc)),
			NumEnums:      0,
			NumMessages:   3,
			NumExtensions: 0,
			NumServices:   0,
		},
		GoTypes:           file_pb_control_proto_goTypes,
		DependencyIndexes: file_pb_control_proto_depIdxs,
		MessageInfos:      file_pb_control_proto_msgTypes,
	}.Build()
	File_pb_control_proto = out.File
	file_pb_control_proto_goTypes = nil
	file_pb_control_proto_depIdxs = nil
}
