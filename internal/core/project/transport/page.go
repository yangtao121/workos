package transport

import commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"

func (p commonPage) proto() *commonv1.PageResponse {
	return &commonv1.PageResponse{NextPageToken: p.NextPageToken}
}
